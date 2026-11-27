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
// registered games ids beside the legacy one, added before it starts as
// Add a game adds one. Their server names are "<Short>'s server".
func setupGames(t *testing.T, ids []string, change ...func(*Options)) *testEnv {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env, change...)
	env.m = m
	for _, id := range ids {
		g, ok := game.Get(id)
		if !ok {
			t.Fatalf("no game %q is registered", id)
		}
		inst := addTestGame(t, m, g, nil)
		if err := inst.srv.Store().Save(func(s *game.Saved) error {
			s.Server = game.Values{"serverName": g.Info().Short + "'s server"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
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

// useServers gives the manager a list of games as R3's manager reads it
// from servers.json: the stars, the games turned off, and each game's own
// channel (a stand-in webhook id, "" for the main channel).
func useServers(m *Manager, stars, off []string, own map[string]string) {
	f := servers.New(m.legacyID, time.Now())
	f.App.Stars, f.App.DisabledGames = append([]string{}, stars...), off
	m.eachInstance(func(inst *instance) {
		if inst.legacy {
			return
		}
		in := servers.Instance{ID: inst.id, Game: inst.id, Added: time.Now()}
		if hook := own[inst.id]; hook != "" {
			d := servers.DefaultDiscord()
			d.Webhook = hookLink(hook)
			in.Discord = &d
		}
		f.Instances = append(f.Instances, in)
	})
	m.mu.Lock()
	m.publishServersLocked(f)
	m.mu.Unlock()
}

// valheimOnly puts the manager in Valheim-only safe mode (spec 8.3), or
// takes it out again (its list of games is then whatever was published
// last). It is the one time this build has no list of games to set the
// waker up from, so it sets it up as R2 does: /valheim alone.
func valheimOnly(m *Manager, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		m.enterSafeModeLocked(time.Now(), "Only Valheim runs: a test.")
		return
	}
	m.safeMode = ""
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

	// No list of games to use (Valheim-only safe mode): /valheim alone, as
	// R2 sets it up, but in the main channel's Discord server all the same.
	valheimOnly(m, true)
	cfg, setup, err := m.wakerPlan(ctx, "secret")
	query := net.JoinHostPort("192.168.1.20", strconv.Itoa(port+1))
	want := waker.Config{Token: "secret", MAC: "3C:07:54:5A:21:9F", Query: query, Name: m.Settings().ServerName, Guild: "g1"}
	if err != nil || !reflect.DeepEqual(cfg, want) || setup.Games != "" || setup.Guild != "g1" {
		t.Fatalf("without a list of games: %+v %+v %v", cfg, setup, err)
	}
	valheimOnly(m, false)

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

// With no list of games (Valheim-only safe mode: /valheim alone, as R2
// sets it up) and Valheim's
// own posts off, nobody posts to the main link: a deleted one, or a Discord
// out of reach, never stops the waker being installed or kept up, and it
// keeps the Discord server it answers in now. With the posts on, the plan
// fails as before R3, naming the join-code channel.
func TestWakerPlanR2BrokenMainLink(t *testing.T) {
	ch := newFakeChannels(t)
	env := setup(t, wakerOptions(t, nil, ch.URL, wired()))
	m := env.m
	ctx := context.Background()
	gone := func(on bool) {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		ch.gone["1"] = on
	}
	valheimOnly(m, true)
	setMain(t, m, "1", false)
	gone(true)
	cfg, setup, err := m.wakerPlan(ctx, "secret")
	if err != nil || cfg.Guild != "" || setup.Guild != "" || cfg.Query == "" || cfg.Games != nil {
		t.Fatalf("deleted link, nothing installed: %+v %+v %v", cfg, setup, err)
	}
	m.mu.Lock()
	m.wk.file.Setup.Guild = "g1" // installed while the link still worked
	m.mu.Unlock()
	if _, setup, err = m.wakerPlan(ctx, "secret"); err != nil || setup.Guild != "g1" {
		t.Fatalf("deleted link: %+v %v", setup, err)
	}
	ch.down.Store(true)
	if _, setup, err = m.wakerPlan(ctx, "secret"); err != nil || setup.Guild != "g1" {
		t.Fatalf("Discord out of reach: %+v %v", setup, err)
	}
	ch.down.Store(false)

	gone(false)
	setMain(t, m, "1", true) // Valheim posts its join code there
	gone(true)
	if _, _, err := m.wakerPlan(ctx, "secret"); err == nil || !strings.Contains(err.Error(), "Couldn’t find out which Discord server the join-code channel is in.") {
		t.Fatalf("deleted link with Valheim's posts on: %v", err)
	}
	gone(false)
	ch.mu.Lock()
	ch.guilds["1"] = "g7" // the channel the link posts in now is the one that counts
	ch.mu.Unlock()
	if _, setup, err = m.wakerPlan(ctx, "secret"); err != nil || setup.Guild != "g7" {
		t.Fatalf("working link: %+v %v", setup, err)
	}
}

// The waker on a stand-in JetKVM follows the stars: unstarring everything
// leaves it with no commands, and a star change while the app is busy with
// the JetKVM is tried again later.
func TestWakerFollowsStars(t *testing.T) {
	set(t, &commandsDelay, 300*time.Millisecond)
	set(t, &commandsRetry, 500*time.Millisecond)
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
	// waits, and goes through once the JetKVM is free. The test takes the
	// JetKVM the way the Settings page does (beginWaker), and only once the
	// update above has wholly finished (it writes the waker's settings
	// before it waits for the waker to start), so no other update can free
	// it meanwhile: only a retry can then bring /minecraft.
	none := commandsPrint([]waker.GameCommand{})
	waitFor(t, "the update to finish and the JetKVM to be free", 20*time.Second, func() bool {
		m.mu.Lock()
		done := m.wk.file.Setup.Games == none
		m.mu.Unlock()
		return done && m.beginWaker() == nil
	})
	useServers(m, []string{"minecraft"}, nil, map[string]string{"minecraft": "2"})
	m.starsChanged() // as SetStar does
	time.Sleep(commandsDelay + 3*commandsRetry)
	m.mu.Lock()
	busy, setup := m.wk.busy, m.wk.file.Setup
	m.mu.Unlock()
	if got := commands(); len(got) != 0 || !busy || setup.Games != none {
		t.Fatalf("updated while busy: %q (busy %v, setup %+v)", got, busy, setup)
	}
	m.endWaker()
	waitFor(t, "/minecraft once the JetKVM is free", 20*time.Second, func() bool {
		return reflect.DeepEqual(commands(), []string{"minecraft@ch2"})
	})
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
	set(t, &startFromRequest, r.start)
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
	set(t, &wakeRepeats, 400*time.Millisecond)
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

// A start Discord asked for whose server dies while it is still loading is
// answered in the channel, whatever Valheim's own posts are set to. It runs
// the real order: the run ends (onExit) before its start job fails (onJob).
// A stop on purpose while it loads is no failure: nobody is told.
func TestWakeStartDiesWhileLoading(t *testing.T) {
	t.Setenv("FAKE_VALHEIM_SPEED", "0.1") // ten times slower: it loads for half a minute
	ch := newFakeChannels(t)
	env := setup(t, wakerOptions(t, nil, ch.URL))
	m := env.m
	install(t, m)
	setMain(t, m, "1", false)
	inst := m.legacy()
	asked := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.askedFromDiscordLocked(inst)
	}
	loading := func() *serverRun {
		var run *serverRun
		waitFor(t, "the server to be loading", 20*time.Second, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			run = inst.run
			return run != nil && !run.ready && running(inst.job) && inst.job.Kind == "start" && m.askedFromDiscordLocked(inst)
		})
		return run
	}

	m.wakeGame(m.legacyID, "crash1")
	if err := loading().proc.Kill(); err != nil { // it crashes while loading
		t.Fatal(err)
	}
	ch.wait(t, "1", "⚠️ **Valheim** didn't start: the server stopped while starting.")
	m.mu.Lock()
	job := *inst.job
	m.mu.Unlock()
	if !strings.HasPrefix(job.Error, "The server stopped while starting.") || asked() {
		t.Fatalf("the start job = %+v, still marked as asked for: %v", job, asked())
	}

	m.wakeGame(m.legacyID, "stop01")
	loading()
	if err := m.StopServer(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the server to stop", 20*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return inst.run == nil && !running(inst.job)
	})
	time.Sleep(500 * time.Millisecond)
	if n := len(ch.in("1")); n != 1 || asked() {
		t.Fatalf("posts %q, still marked as asked for: %v", ch.in("1"), asked())
	}
}

// A start job that fails at once (a port in use, say) can end before the
// request is marked as asked for from Discord: it is answered all the same,
// once, in the game's channel. The stand-in start makes its job end before
// it returns, as such a job can.
func TestWakeFailedStartIsAnswered(t *testing.T) {
	ch := newFakeChannels(t)
	env := setupGames(t, []string{"minecraft"}, wakerOptions(t, nil, ch.URL))
	m := env.m
	setMain(t, m, "1", true)
	useServers(m, []string{"minecraft"}, nil, map[string]string{"minecraft": "2"})
	inst, _ := m.instanceByID("minecraft")
	useRequests(t, &requests{answer: func(id string, n int) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := time.Now()
		job := &Job{ID: 1000 + n, Game: id, Kind: "start", Started: now, Percent: -1}
		inst.job = job
		m.observeLocked(func(o observer) { o.onJob(inst, job) }) // started, as jobs.go says
		job.Finished, job.Error = &now, "Port 25565 is in use by another program."
		m.observeLocked(func(o observer) { o.onJob(inst, job) }) // and finished
		return nil
	}})
	m.wakeGame("minecraft", "mc0001")
	ch.wait(t, "2", "⚠️ **Minecraft** didn't start: port 25565 is in use by another program.")
	m.wakeGame("minecraft", "mc0001") // the waker's repeat
	time.Sleep(300 * time.Millisecond)
	m.mu.Lock()
	asked := m.askedFromDiscordLocked(inst)
	m.mu.Unlock()
	if asked || len(ch.in("2")) != 1 {
		t.Fatalf("still marked as asked for: %v; posts %q", asked, ch.in("2"))
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

// A game a Discord request started posts its online message in the
// channel the request came from, even with its own posts off, because
// someone asked there (spec 6.4); only once, however often the waker
// repeats the request. The real start runs here (no stand-in).
func TestWakeStartPostsOnline(t *testing.T) {
	ch := newFakeChannels(t)
	env := setup(t, wakerOptions(t, nil, ch.URL))
	m := env.m
	install(t, m)
	setMain(t, m, "1", false) // Valheim's own posts are off
	m.wakeGame(m.legacyID, "vh0001")
	ch.wait(t, "1", "is online")
	m.wakeGame(m.legacyID, "vh0001") // the waker's repeat
	waitFor(t, "the start job to end", 20*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !running(m.legacy().job)
	})
	time.Sleep(500 * time.Millisecond)
	if posts := ch.in("1"); len(posts) != 1 || !strings.HasPrefix(posts[0], "🟢 **") {
		t.Fatalf("posts in the request's channel: %q", posts)
	}
}

// A request kept while its game was busy, whose game someone started
// meanwhile, is met at the last try: the channel hears it is already
// online, and nothing claims the request started it.
func TestWakeKeptRequestFindsItOnline(t *testing.T) {
	set(t, &wakeRepeats, time.Hour) // the test makes the last try itself, below
	ch := newFakeChannels(t)
	env := setup(t, wakerOptions(t, nil, ch.URL))
	m := env.m
	install(t, m)
	setMain(t, m, "1", true)
	inst := m.legacy()
	release := make(chan struct{})
	if err := m.runInstJob(inst, "backup", "Backing up", false, func(context.Context, *jobCtl) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.wakeGame(m.legacyID, "vh0001") // busy: kept
	close(release)
	waitFor(t, "the backup job", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !running(inst.job)
	})
	startOnline(t, m) // the owner starts it from the panel
	waitFor(t, "the start job to end", 20*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !running(inst.job)
	})
	m.startForRequest(inst, "vh0001", true) // the try once the waker's repeats are over
	ch.wait(t, "1", "✅ **Valheim** is already online.")
	if hasText(m, "info", "Starting the server: someone asked for it from Discord") {
		t.Fatal("the request is reported as having started the server")
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
