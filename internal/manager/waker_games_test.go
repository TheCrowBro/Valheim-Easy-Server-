package manager

// The waker with several games (spec 6.4): a command per starred game that
// has a channel, each working only there, replies in that channel, and
// waking one game starts only that game.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/jetkvm/fakekvm"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/waker"
	"valheim-easy-server/internal/wol"
	"valheim-easy-server/internal/worlds"
)

// fakeChannels stands in for Discord's webhooks, each in its own channel:
// a GET says which Discord server and channel it posts in ("g1" and
// "ch<id>" unless guilds says otherwise), a POST is a message there.
type fakeChannels struct {
	URL    string
	mu     sync.Mutex
	posts  map[string][]string // by webhook id
	guilds map[string]string   // webhook id → Discord server
	gone   map[string]bool     // webhooks that were deleted
	down   atomic.Bool         // Discord can't be reached
}

func newFakeChannels(t *testing.T) *fakeChannels {
	f := &fakeChannels{posts: map[string][]string{}, guilds: map[string]string{}, gone: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/webhooks/{id}/{token}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case f.gone[id]:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message": "Unknown Webhook", "code": 10015}`))
			return
		case r.Method == http.MethodGet:
			g := f.guilds[id]
			if g == "" {
				g = "g1"
			}
			fmt.Fprintf(w, `{"id": %q, "type": 1, "guild_id": %q, "channel_id": "ch%s"}`, id, g, id)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.posts[id] = append(f.posts[id], body.Content)
		w.Write([]byte(`{"id": "1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// hookLink is the webhook link of the stand-in channel id.
func hookLink(id string) string { return "https://discord.com/api/webhooks/" + id + "/tok" + id }

func (f *fakeChannels) in(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts[id]...)
}

func (f *fakeChannels) wait(t *testing.T, id, text string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%q in channel %s", text, id), 20*time.Second, func() bool {
		for _, p := range f.in(id) {
			if strings.Contains(p, text) {
				return true
			}
		}
		return false
	})
}

// setupGames starts a manager like setup, with an instance of each of the
// games ids beside the legacy one, made before it starts (in this build
// only the legacy instance is made by New; servers.json comes with R3's
// manager). Their server names are "<Short>'s server".
func setupGames(t *testing.T, ids []string, change ...func(*Options)) *testEnv {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env, change...)
	env.m = m
	for _, id := range ids {
		addTestGame(t, m, id)
	}
	s := m.Settings()
	s.Port = 20000 + rand.Intn(20000)
	s.SaveIntervalMinutes = 1
	s.UpdateOnStart = false
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	m.Start()
	t.Cleanup(func() {
		if env.m.ForceStop() == nil {
			waitFor(t, "forced stop", 10*time.Second, func() bool { return env.m.Status().State != StateOnline })
		}
		env.m.Close()
	})
	return env
}

// addTestGame adds an instance of the registered game id, as R3's manager
// makes one for a servers.json entry, with its folders under games/<id>.
func addTestGame(t *testing.T, m *Manager, id string) *instance {
	t.Helper()
	g, ok := game.Get(id)
	if !ok {
		t.Fatalf("no game %q is registered", id)
	}
	info := g.Info()
	root := filepath.Join(m.opt.DataDir, "games", id)
	dirs := game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Runtime: filepath.Join(root, "runtime"),
		Staging: filepath.Join(root, "staging"), SteamCMD: filepath.Join(m.opt.DataDir, "steamcmd")}
	inst := &instance{id: id, g: g, info: info, dirs: dirs, known: map[string]*KnownPlayer{},
		uploads: map[string]*worlds.Upload{}, logs: newRing[LogLine](100)}
	inst.srv = g.New(game.Env{Instance: id, Data: m.opt.DataDir, Dirs: dirs, Log: m.log, Store: &testStore{}})
	if err := inst.srv.Store().Save(func(s *game.Saved) error {
		s.Server = game.Values{"serverName": info.Short + "'s server"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.instances[id] = inst
	m.order = append(m.order, id)
	return inst
}

// testStore keeps a test game's settings in memory, as servers.json would.
type testStore struct {
	mu sync.Mutex
	s  game.Saved
}

func (s *testStore) Get() game.Saved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.Clone()
}

func (s *testStore) Save(change func(*game.Saved) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.s.Clone()
	if err := change(&next); err != nil {
		return err
	}
	s.s = next
	return nil
}

// useServers gives the manager a list of games as R3's manager reads it
// from servers.json: the stars, the games turned off, and each game's own
// channel (a stand-in webhook id, "" for the main channel).
func useServers(m *Manager, stars, off []string, own map[string]string) {
	f := servers.New(m.legacyID, time.Now())
	f.App.Stars, f.App.DisabledGames = append([]string{}, stars...), off
	for _, id := range m.order[1:] {
		in := servers.Instance{ID: id, Game: id, Added: time.Now()}
		if hook := own[id]; hook != "" {
			d := servers.DefaultDiscord()
			d.Webhook = hookLink(hook)
			in.Discord = &d
		}
		f.Instances = append(f.Instances, in)
	}
	m.mu.Lock()
	m.servers = f
	m.mu.Unlock()
}

// setMain sets the main Discord channel; posts tells whether Valheim's own
// posts go there.
func setMain(t *testing.T, m *Manager, hook string, posts bool) {
	t.Helper()
	s := m.Settings()
	s.Discord, s.DiscordWebhook = posts, ""
	if hook != "" {
		s.DiscordWebhook = hookLink(hook)
	}
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
}

func wired() platform.NetAdapter {
	return platform.NetAdapter{Name: "Ethernet", MAC: "3C:07:54:5A:21:9F", IPv4: []string{"192.168.1.20"}, Wired: true, Connected: true}
}

// The waker's settings with several games: a command for each starred game
// that runs here, is turned on and has a channel, each with that channel;
// the bot answers only in the main channel's Discord server, even with
// Valheim's own posts off; a channel in another Discord server gets no
// command, and the Discord page says why.
func TestWakerPlanGames(t *testing.T) {
	ch := newFakeChannels(t)
	ch.guilds["9"] = "g2"
	env := setupGames(t, []string{"minecraft", "terraria", "necesse", "corekeeper"}, wakerOptions(t, nil, ch.URL, wired()))
	m := env.m
	setMain(t, m, "1", false) // Valheim's own posts are off
	port := m.Settings().Port
	ctx := context.Background()

	// No list of games (a copy without servers.json): /valheim alone, as R2
	// sets it up, but in the main channel's Discord server all the same.
	cfg, setup, err := m.wakerPlan(ctx, "secret")
	query := net.JoinHostPort("192.168.1.20", strconv.Itoa(port+1))
	want := waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F", Query: query, Name: m.Settings().ServerName, Guild: "g1"}
	if err != nil || !reflect.DeepEqual(cfg, want) || setup.Games != "" || setup.Guild != "g1" {
		t.Fatalf("without a list of games: %+v %+v %v", cfg, setup, err)
	}

	useServers(m, []string{"valheim", "minecraft", "terraria", "corekeeper"}, []string{"corekeeper"},
		map[string]string{"minecraft": "2", "terraria": "9"})
	cfg, setup, err = m.wakerPlan(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	games := []waker.GameCommand{
		{ID: "valheim", Command: "valheim", Description: "Wake the server computer and start Valheim", Name: m.Settings().ServerName, Channel: "ch1", Query: query},
		{ID: "minecraft", Command: "minecraft", Description: "Wake the server computer and start Minecraft", Name: "Minecraft's server", Channel: "ch2"},
	}
	if cfg.Games == nil || !reflect.DeepEqual(*cfg.Games, games) || cfg.Query != "" || cfg.Name != "" || cfg.Guild != "g1" {
		t.Fatalf("the waker's settings = %+v (games %+v)", cfg, cfg.Games)
	}
	if setup.Query != "" || setup.Name != "" || setup.Games == "" || setup.Guild != "g1" {
		t.Fatalf("setup = %+v", setup)
	}
	// The same commands give the same setup, so the upkeep leaves the
	// waker alone; it is still a plain comparable struct.
	_, again, _ := m.wakerPlan(ctx, "secret")
	if again != setup {
		t.Fatalf("setup changed by itself: %+v then %+v", setup, again)
	}
	v := m.WakerView(false)
	wantView := []WakerCommand{{Game: "valheim", Command: "valheim"}, {Game: "minecraft", Command: "minecraft"},
		{Game: "terraria", Command: "terraria", Problem: "This channel is in another Discord server, so /terraria won't work there."}}
	if !reflect.DeepEqual(v.Commands, wantView) || !v.CanPost {
		t.Fatalf("the Discord page's commands = %+v (can post %v)", v.Commands, v.CanPost)
	}
	m.mu.Lock()
	if got := m.wakerCommandsLocked(); len(got) != 3 || got[2].Command != "terraria" || got[0].Channel != "" {
		t.Errorf("wakerCommandsLocked = %+v", got)
	}
	m.mu.Unlock()

	// Minecraft's server is renamed: its command changes, so does the setup.
	mc, _ := m.instanceByID("minecraft")
	mc.srv.Store().Save(func(s *game.Saved) error { s.Server["serverName"] = "Blocks"; return nil })
	_, renamed, _ := m.wakerPlan(ctx, "secret")
	if renamed.Games == setup.Games {
		t.Fatal("a renamed server didn't change the waker's setup")
	}

	// Minecraft's channel was deleted: it gets no command until it's fixed,
	// the others keep theirs. Discord down: nothing changes for now.
	ch.mu.Lock()
	ch.gone["2"] = true
	ch.mu.Unlock()
	cfg, _, err = m.wakerPlan(ctx, "secret")
	if err != nil || len(*cfg.Games) != 1 || (*cfg.Games)[0].Command != "valheim" {
		t.Fatalf("with Minecraft's channel deleted: %+v %v", cfg.Games, err)
	}
	if p := m.WakerView(false).Commands[1].Problem; !strings.HasPrefix(p, "/minecraft can't work until its Discord channel works again. Discord says the webhook link doesn't work.") {
		t.Fatalf("problem = %q", p)
	}
	ch.down.Store(true)
	if _, _, err := m.wakerPlan(ctx, "secret"); err == nil {
		t.Fatal("no error while Discord can't be reached")
	}
}

// Nothing starred, or a starred game with no channel at all: no commands,
// written as an empty list so the waker takes every command away, never a
// leftover /valheim; the Discord page says what Minecraft needs.
func TestWakerPlanNoCommands(t *testing.T) {
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, nil, newFakeChannels(t).URL, wired()))
	m := env.m
	useServers(m, []string{}, nil, nil)
	cfg, setup, err := m.wakerPlan(context.Background(), "secret")
	if err != nil || cfg.Games == nil || len(*cfg.Games) != 0 || setup.Games == "" || cfg.Guild != "" {
		t.Fatalf("nothing starred: %+v %+v %v", cfg, setup, err)
	}
	if b, _ := json.Marshal(cfg); !strings.Contains(string(b), `"games":[]`) || strings.Contains(string(b), `"query"`) {
		t.Fatalf("written as %s", b)
	}
	if v := m.WakerView(false); v.Commands == nil || len(v.Commands) != 0 {
		t.Fatalf("commands = %+v", v.Commands)
	}

	useServers(m, []string{"minecraft"}, nil, nil) // starred, but no channel anywhere
	cfg, _, err = m.wakerPlan(context.Background(), "secret")
	if err != nil || len(*cfg.Games) != 0 {
		t.Fatalf("Minecraft with no channel: %+v %v", cfg.Games, err)
	}
	want := []WakerCommand{{Game: "minecraft", Command: "minecraft", Problem: "Add a Discord channel so /minecraft can work."}}
	if v := m.WakerView(false); !reflect.DeepEqual(v.Commands, want) {
		t.Fatalf("commands = %+v", v.Commands)
	}
}

// The waker on a stand-in JetKVM follows the stars: unstarring everything
// leaves it with no commands, and a star change while the app is busy with
// the JetKVM is tried again later.
func TestWakerFollowsStars(t *testing.T) {
	setVar(t, &commandsDelay, 300*time.Millisecond)
	setVar(t, &commandsRetry, 500*time.Millisecond)
	kvm := fakekvm.New(t, nil)
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, kvm, ch.URL, wired()))
	m := env.m
	setMain(t, m, "1", true)
	useServers(m, []string{"valheim", "minecraft"}, nil, map[string]string{"minecraft": "2"})
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(m.WakerView(false).Key))
	if err != nil {
		t.Fatal(err)
	}
	kvm.Allow(key)
	if err := m.InstallWaker(kvm.Addr, "secret", false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kvm.Base, "valheim-waker", "config.json")
	commands := func() []string {
		c, err := waker.LoadConfig(path)
		if err != nil || c.Games == nil {
			return nil
		}
		names := []string{}
		for _, g := range *c.Games {
			names = append(names, g.Command+"@"+g.Channel)
		}
		return names
	}
	if got := commands(); !reflect.DeepEqual(got, []string{"valheim@ch1", "minecraft@ch2"}) {
		t.Fatalf("commands on the JetKVM = %q", got)
	}
	if !hasText(m, "info", "The waker is on the JetKVM: friends can type /valheim or /minecraft in Discord") {
		t.Fatal("no activity naming the commands")
	}
	var file wakerFile
	data, _ := os.ReadFile(filepath.Join(env.data, "jetkvm.json"))
	if json.Unmarshal(data, &file) != nil || file.Setup.Games == "" || !strings.Contains(string(data), `"games"`) {
		t.Fatalf("jetkvm.json = %s", data)
	}

	// Everything unstarred (seen by the sample loop, not told): no commands.
	useServers(m, []string{}, nil, map[string]string{"minecraft": "2"})
	waitFor(t, "the commands to go", 20*time.Second, func() bool {
		got := commands()
		return got != nil && len(got) == 0
	})
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"games": []`) {
		t.Fatalf("the waker's settings = %s", data)
	}

	// Minecraft starred while the app is busy with the JetKVM: the update
	// waits, and goes through once the JetKVM is free.
	m.mu.Lock()
	m.wk.busy = true
	m.mu.Unlock()
	useServers(m, []string{"minecraft"}, nil, map[string]string{"minecraft": "2"})
	m.starsChanged() // as SetStar does
	time.Sleep(4 * commandsRetry)
	if got := commands(); len(got) != 0 {
		t.Fatalf("updated while busy: %q", got)
	}
	m.mu.Lock()
	m.wk.busy = false
	m.mu.Unlock()
	waitFor(t, "/minecraft once the JetKVM is free", 20*time.Second, func() bool {
		return reflect.DeepEqual(commands(), []string{"minecraft@ch2"})
	})
}

func setVar[T any](t *testing.T, v *T, to T) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// requests records the starts that Discord requests asked for, and answers
// each with what answer says.
type requests struct {
	mu     sync.Mutex
	got    []string
	answer func(id string, n int) error
}

func (r *requests) start(m *Manager, id, tag string) error {
	r.mu.Lock()
	r.got = append(r.got, id+"/"+tag)
	n := len(r.got)
	answer := r.answer
	r.mu.Unlock()
	if answer != nil {
		return answer(id, n)
	}
	return nil
}

func (r *requests) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func useRequests(t *testing.T, r *requests) {
	setVar(t, &startFromRequest, r.start)
}

// A request for one game starts only that game, once however often the
// waker repeats it; an older waker's packet (no game) is Valheim's; a game
// this copy doesn't run starts nothing; the KVM's own button starts none.
func TestWakeStartsOnlyThatGame(t *testing.T) {
	mac := localMAC()
	if mac == nil {
		t.Skip("no network adapter with an address")
	}
	env := setupGames(t, []string{"minecraft"})
	m := env.m
	r := &requests{}
	useRequests(t, r)
	rec := listen(m)
	m.hearWakeSignals(&scriptedConn{reads: []any{
		wol.PacketFor(mac, []byte("mc0001"), "minecraft"),
		wol.PacketFor(mac, []byte("mc0001"), "minecraft"), // the waker's repeat
		wol.PacketFor(mac, []byte("pw0001"), "palworld"),  // not a game here
		wol.PacketFor(mac, []byte("vh0001"), "valheim"),
		wol.Packet(mac, []byte("old001")), // an older waker: Valheim
		wol.Packet(mac, nil),              // the KVM's own button
		wol.PacketFor(mac, []byte("mc0002"), "minecraft"),
	}})
	want := []string{"minecraft/mc0001", "valheim/vh0001", "valheim/old001", "minecraft/mc0002"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("starts asked for: %q, want %q", got, want)
	}
	m.mu.Lock()
	asked := m.wakeAsked
	m.mu.Unlock()
	if asked.IsZero() || !rec.has("wake tagged") || !rec.has("wake untagged") {
		t.Fatalf("asked at %v; observers saw:\n%s", asked, rec)
	}
	if !hasText(m, "info", "Starting Minecraft: someone asked for it from Discord (/minecraft).") {
		t.Fatal("no activity about Minecraft's start")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.askedFromDiscordLocked(m.instances["minecraft"]) {
		t.Fatal("Minecraft's start isn't marked as asked for from Discord")
	}
}

// A start Discord asked for that is refused is answered in the game's own
// channel, once per request, whatever the game's own posts are set to.
func TestWakeRefusalIsAnswered(t *testing.T) {
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft", "palworld"}, wakerOptions(t, nil, ch.URL))
	m := env.m
	setMain(t, m, "1", false)
	useServers(m, []string{"valheim", "minecraft", "palworld"}, nil, map[string]string{"palworld": "5"})
	r := &requests{answer: func(id string, n int) error {
		switch id {
		case "palworld":
			return &ActionError{Msg: "There may not be enough memory to start Palworld: it needs about 10 GB and 6.2 GB is free.", Action: "memory",
				Data: MemoryShort{NeedMB: 10240, FreeMB: 6349, Running: []RunningGame{{ID: "valheim", Name: "Valheim", MemMB: 3170}, {ID: "minecraft", Name: "Minecraft", MemMB: 2380}}}}
		case "minecraft":
			inst, _ := m.instanceByID(id)
			return errNotInstalled(inst)
		}
		return nil
	}}
	useRequests(t, r)
	m.wakeGame("palworld", "pw0001")
	m.wakeGame("palworld", "pw0001") // the waker's repeat
	ch.wait(t, "5", "🧠 There isn't enough memory to start **Palworld** right now. Running: Valheim (3.1 GB), Minecraft (2.3 GB).")
	m.wakeGame("minecraft", "mc0001") // Minecraft posts in the main channel
	ch.wait(t, "1", "⚠️ **Minecraft** didn't start: the Minecraft server isn't installed yet.")
	time.Sleep(300 * time.Millisecond)
	if n := len(ch.in("5")); n != 1 || len(r.list()) != 2 {
		t.Fatalf("%d posts for Palworld, starts %q", n, r.list())
	}
	if !hasText(m, "warning", "Palworld didn't start for a request from Discord.") {
		t.Fatal("the activity feed doesn't say why Palworld didn't start")
	}
}

// A game turned off starts from no request, and the channel is told why; a
// Valheim removed from the panel starts from none either (I1), even an
// older waker's.
func TestWakeTurnedOffOrHidden(t *testing.T) {
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, nil, ch.URL))
	m := env.m
	setMain(t, m, "1", true)
	useServers(m, []string{"minecraft"}, []string{"minecraft"}, nil)
	m.mu.Lock()
	m.servers.Instance(m.legacyID).Hidden = true
	m.mu.Unlock()
	r := &requests{}
	useRequests(t, r)
	m.wakeGame("minecraft", "mc0001")
	m.wakeGame("minecraft", "mc0001")
	m.wakeSignal("vh0001")
	ch.wait(t, "1", "⚠️ **Minecraft** didn't start: Minecraft is turned off on its Settings tab.")
	time.Sleep(300 * time.Millisecond)
	if got := r.list(); len(got) != 0 || len(ch.in("1")) != 1 {
		t.Fatalf("starts %q, posts %q", got, ch.in("1"))
	}
}

// A request heard while its game is busy is kept: a repeat starts it once
// it can; if none could by the time the repeats are over, the app tries
// once more and says why in the channel.
func TestWakeWhileBusy(t *testing.T) {
	setVar(t, &wakeRepeats, 400*time.Millisecond)
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, nil, ch.URL))
	m := env.m
	setMain(t, m, "1", true)
	useServers(m, []string{"minecraft"}, nil, nil)
	var free atomic.Bool
	r := &requests{answer: func(id string, n int) error {
		if free.Load() {
			return nil
		}
		return &BusyError{Title: "installing the server"}
	}}
	useRequests(t, r)

	// Busy for good: one answer, after the repeats.
	m.wakeGame("minecraft", "mc0001")
	m.wakeGame("minecraft", "mc0001")
	if n := len(ch.in("1")); n != 0 {
		t.Fatal("answered while the waker was still repeating")
	}
	ch.wait(t, "1", "⚠️ **Minecraft** didn't start: installing the server is still in progress. Ask again once it's done.")
	// Free before the repeats are over: the repeat starts it, nobody is
	// told it didn't.
	m.wakeGame("minecraft", "mc0002")
	free.Store(true)
	m.wakeGame("minecraft", "mc0002")
	time.Sleep(3 * wakeRepeats)
	if n := len(ch.in("1")); n != 1 {
		t.Fatalf("posts: %q", ch.in("1"))
	}
	want := []string{"minecraft/mc0001", "minecraft/mc0001", "minecraft/mc0001", "minecraft/mc0002", "minecraft/mc0002"}
	if got := r.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("starts asked for: %q", got)
	}
}

// A start Discord asked for that fails in its job (the server stopped while
// starting, say) is answered in the channel too.
func TestWakeFailedStartIsAnswered(t *testing.T) {
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, nil, ch.URL))
	m := env.m
	setMain(t, m, "1", true)
	useServers(m, []string{"minecraft"}, nil, map[string]string{"minecraft": "2"})
	useRequests(t, &requests{})
	m.wakeGame("minecraft", "mc0001")
	inst, _ := m.instanceByID("minecraft")
	now := time.Now()
	m.mu.Lock()
	m.observeLocked(func(o observer) {
		o.onJob(inst, &Job{Kind: "start", Error: "The server stopped while starting.", Finished: &now})
	})
	asked := m.askedFromDiscordLocked(inst)
	m.mu.Unlock()
	ch.wait(t, "2", "⚠️ **Minecraft** didn't start: the server stopped while starting.")
	if asked {
		t.Fatal("still marked as asked for after its start failed")
	}
}

// A request for a game that is already online says so in its channel, with
// how to join, rather than starting it again.
func TestWakeAlreadyOnline(t *testing.T) {
	hook := newFakeDiscord(t)
	env := setup(t, func(o *Options) { o.DiscordURL = hook.URL })
	m := env.m
	install(t, m)
	turnOnDiscord(t, m)
	code := startOnline(t, m)
	waitFor(t, "the start job to end", 20*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !running(m.legacy().job)
	})
	jobs := currentJobID(m)
	m.wakeGame(m.legacyID, "again1")
	hook.wait(t, "✅ **Valheim** is already online.", 1)
	for _, p := range hook.all() {
		if strings.HasPrefix(p, "✅") && (!strings.Contains(p, code) || strings.Contains(p, "🟢")) {
			t.Fatalf("the answer = %q, want the join code %s and no second headline", p, code)
		}
	}
	m.wakeGame(m.legacyID, "again1") // the repeat
	time.Sleep(300 * time.Millisecond)
	if hook.count("is already online") != 1 || currentJobID(m) != jobs {
		t.Fatalf("posts %q, job %d then %d", hook.all(), jobs, currentJobID(m))
	}
}

func TestWakeReplyText(t *testing.T) {
	running := []RunningGame{{ID: "valheim", Name: "Valheim", MemMB: 3170}, {ID: "terraria", Name: "Terraria", MemMB: 800}}
	for _, c := range []struct {
		err  error
		want string
	}{
		{&ActionError{Msg: "…", Action: "memory", Data: MemoryShort{Running: []RunningGame{{Name: "Valheim", MemMB: 3170}, {Name: "Minecraft", MemMB: 2380}}}},
			"🧠 There isn't enough memory to start **Palworld** right now. Running: Valheim (3.1 GB), Minecraft (2.3 GB)."},
		{&ActionError{Msg: "…", Action: "memory", Data: &MemoryShort{}},
			"🧠 There isn't enough memory to start **Palworld** right now."},
		{&ActionError{Msg: "…", Action: "memory"},
			"🧠 There isn't enough memory to start **Palworld** right now. Running: Valheim (3.1 GB), Terraria (800 MB)."},
		{&BusyError{Title: "installing the server"},
			"⚠️ **Palworld** didn't start: installing the server is still in progress. Ask again once it's done."},
		{errors.New("This game is turned off. Turn it on on its Settings tab."),
			"⚠️ **Palworld** didn't start: this game is turned off. Turn it on on its Settings tab."},
		{errors.New("Palworld needs Windows"),
			"⚠️ **Palworld** didn't start: Palworld needs Windows."},
		{errStillStopping,
			"⚠️ **Palworld** didn't start: it was still saving and shutting down. Ask again in a minute."},
	} {
		if got := wakeReplyText("Palworld", c.err, running); got != c.want {
			t.Errorf("%v:\n got %q\nwant %q", c.err, got, c.want)
		}
	}
	if got := alreadyOnlineText("Valheim", "🟢 **Viking Night** is online\nWorld: Midgard\nJoin code: **123456**"); got != "✅ **Valheim** is already online.\nWorld: Midgard\nJoin code: **123456**" {
		t.Errorf("already online = %q", got)
	}
	// wakerSetup stays comparable with ==, which is how the upkeep tells
	// whether the waker needs installing again.
	a, b := wakerSetup{Games: commandsPrint([]waker.GameCommand{{ID: "valheim"}})}, wakerSetup{Games: commandsPrint([]waker.GameCommand{{ID: "valheim"}})}
	if a != b || a == (wakerSetup{Games: commandsPrint([]waker.GameCommand{})}) {
		t.Error("the commands' fingerprint isn't stable")
	}
}
