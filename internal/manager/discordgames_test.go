package manager

// Discord with several games (spec 6.3): a channel per game falling back to
// the main one, a queue per channel, daily posts per game, and the texts
// other games use. The games here are stand-ins made in the test (this
// release's manager doesn't add other games yet), with just enough of an
// adapter for their posts.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/discord"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/worlds"
)

// --- a stand-in Discord ----------------------------------------------------------

// standIn answers like Discord: webhooks (posts, and which Discord server
// and channel each is in) and, for a bot, a channel's messages and name.
// Webhook n posts in channel "90n", in Discord server "g1" unless guilds
// says otherwise.
type standIn struct {
	URL   string
	clock func() time.Time // gives the time new messages' ids carry

	mu     sync.Mutex
	posts  []standInPost
	down   map[string]bool              // webhook ids that answer like Discord having trouble
	guilds map[string]string            // webhook id → Discord server
	msgs   map[string][]discord.Message // by channel
	deny   bool                         // reading messages answers 403 (the channel's name still answers)
	reads  int
	seq    uint64
}

type standInPost struct {
	Hook    string // the webhook's id
	Content string
}

func newStandIn(t *testing.T, clock func() time.Time) *standIn {
	s := &standIn{clock: clock, down: map[string]bool{}, guilds: map[string]string{}, msgs: map[string][]discord.Message{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/webhooks/{id}/{token}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		down := s.down[id]
		s.mu.Unlock()
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		msg := discord.Message{ID: s.newIDLocked(), WebhookID: id}
		msg.Author.ID, msg.Author.Bot = id, true
		s.posts = append(s.posts, standInPost{Hook: id, Content: body.Content})
		s.msgs[channelOf(id)] = append(s.msgs[channelOf(id)], msg)
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": msg.ID})
	})
	mux.HandleFunc("GET /api/webhooks/{id}/{token}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		guild := s.guilds[id]
		s.mu.Unlock()
		if guild == "" {
			guild = "g1"
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id, "guild_id": guild, "channel_id": channelOf(id)})
	})
	bot := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bot tok" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message": "401: Unauthorized", "code": 0}`))
			return false
		}
		return true
	}
	mux.HandleFunc("GET /bot/channels/{ch}/messages", func(w http.ResponseWriter, r *http.Request) {
		if !bot(w, r) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reads++
		if s.deny {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message": "Missing Access", "code": 50001}`))
			return
		}
		after := r.URL.Query().Get("after")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		out := []discord.Message{}
		for _, msg := range s.msgs[r.PathValue("ch")] {
			if discord.Later(msg.ID, after) && len(out) < limit {
				out = append(out, msg)
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /bot/channels/{ch}", func(w http.ResponseWriter, r *http.Request) {
		if bot(w, r) {
			json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("ch"), "name": "chat-" + r.PathValue("ch")})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func channelOf(hook string) string { return "90" + hook }

// newIDLocked is a new message's id, from the stand-in's clock, so ids go
// up as the pretend time does.
func (s *standIn) newIDLocked() string {
	base, _ := strconv.ParseUint(discord.SnowflakeAt(s.clock()), 10, 64)
	s.seq++
	return strconv.FormatUint(base+s.seq, 10)
}

// say posts a message in the webhook's channel as a person would.
func (s *standIn) say(hook string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := discord.Message{ID: s.newIDLocked()}
	msg.Author.ID = "friend"
	s.msgs[channelOf(hook)] = append(s.msgs[channelOf(hook)], msg)
}

// botSays posts a message in the webhook's channel as a bot would.
func (s *standIn) botSays(hook string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := discord.Message{ID: s.newIDLocked()}
	msg.Author.ID, msg.Author.Bot = "waker", true
	s.msgs[channelOf(hook)] = append(s.msgs[channelOf(hook)], msg)
}

// setDeny makes reading messages answer 403, as for a bot without Read
// Message History in the channel.
func (s *standIn) setDeny(on bool) {
	s.mu.Lock()
	s.deny = on
	s.mu.Unlock()
}

func (s *standIn) setDown(hook string, on bool) {
	s.mu.Lock()
	s.down[hook] = on
	s.mu.Unlock()
}

func (s *standIn) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// postsIn is what was posted through the webhook, in order.
func (s *standIn) postsIn(hook string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.posts {
		if p.Hook == hook {
			out = append(out, p.Content)
		}
	}
	return out
}

func (s *standIn) all() []standInPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]standInPost(nil), s.posts...)
}

// count is how many posts through any webhook contain text.
func (s *standIn) count(text string) int {
	n := 0
	for _, p := range s.all() {
		if strings.Contains(p.Content, text) {
			n++
		}
	}
	return n
}

// The webhook links of the tests: hook n is webhook id n.
func hookLink(n int) string {
	return "https://discord.com/api/webhooks/" + strconv.Itoa(n) + "/tok_" + strconv.Itoa(n)
}

const (
	mainHook  = "1" // the main channel
	otherHook = "2" // a game's own channel
)

// --- a pretend clock -------------------------------------------------------------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// --- stand-in games ----------------------------------------------------------------

// testGame is a game added beside Valheim in these tests.
type testGame struct{ info game.Info }

func (g testGame) Info() game.Info { return g.info }

func (g testGame) New(env game.Env) game.Server {
	return &testServer{info: g.info, dir: env.Dirs.Server, store: &testStore{saved: game.Saved{
		Server: game.Values{"serverName": g.info.Name + " Night"}, World: "Home"}}}
}

// testServer is just enough of an adapter for Discord's posts, the records
// and Status. Anything else isn't called in these tests (it would panic).
type testServer struct {
	game.Server
	info  game.Info
	dir   string
	store *testStore
}

func (s *testServer) Store() game.Store                            { return s.store }
func (s *testServer) Program() string                              { return filepath.Join(s.dir, "not-installed") }
func (s *testServer) Texts() game.Texts                            { return game.Texts{Players: "players", Player: "player"} }
func (s *testServer) Fingerprint(game.Saved, string) string        { return "" }
func (s *testServer) Validate(game.Saved, string) game.FieldErrors { return nil }
func (s *testServer) Join(in game.JoinInput) game.Join {
	j := game.Join{How: "address", Port: 7777, LAN: []string{}, Steps: []string{}, Hint: "In " + s.info.Short + ", choose Join."}
	if s.info.Presence == game.Unknown { // like Windrose: an invite code
		j.How, j.CodeLabel, j.Code, j.Port = "code", "Invite code", "Ab12Cd", 0
	}
	return j
}

type testStore struct {
	mu    sync.Mutex
	saved game.Saved
}

func (s *testStore) Get() game.Saved {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved.Clone()
}

func (s *testStore) Save(change func(*game.Saved) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.saved.Clone()
	if err := change(&c); err != nil {
		return err
	}
	s.saved = c
	return nil
}

// Two stand-in games: one whose players can be counted and that has no
// deaths (like Terraria), and one that can't tell who is on (like Windrose).
var (
	blockland = game.Info{ID: "blockland", Name: "Blockland", Short: "Blockland", Presence: game.ByName, MaxPlayers: 8}
	seafarer  = game.Info{ID: "seafarer", Name: "Seafarer", Short: "Seafarer", Presence: game.Unknown, MaxPlayers: 4}
	tundra    = game.Info{ID: "tundra", Name: "Tundra", Short: "Tundra", Presence: game.ByName, Deaths: true, MaxPlayers: 8}
)

// addTestGame adds a stand-in game's instance, as Add a game will, with
// its servers.json entry (d nil: the game's Discord settings were never
// changed). Like New, it reads none of the game's files: its records are
// read the first time they're needed. It must be called before Start: the
// instances aren't guarded in this release.
func addTestGame(t *testing.T, m *Manager, info game.Info, d *servers.Discord) *instance {
	t.Helper()
	g := testGame{info}
	root := filepath.Join(m.opt.DataDir, "games", info.ID)
	dirs := game.Dirs{Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"), Staging: filepath.Join(root, "uploads")}
	inst := &instance{id: info.ID, g: g, info: info, dirs: dirs, known: map[string]*KnownPlayer{},
		uploads: map[string]*worlds.Upload{}, logs: newRing[LogLine](100)}
	inst.srv = g.New(game.Env{Instance: info.ID, Data: m.opt.DataDir, Dirs: dirs, Log: m.log})
	m.instances[inst.id] = inst
	m.order = append(m.order, inst.id)
	m.servers.Instances = append(m.servers.Instances, servers.Instance{ID: info.ID, Game: info.ID, Added: time.Now(), Discord: d})
	return inst
}

// gamesManager makes a manager whose Discord is the stand-in; before runs
// before it starts (add games, set settings).
func gamesManager(t *testing.T, api *standIn, before func(m *Manager)) *Manager {
	t.Helper()
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env, func(o *Options) {
		o.DiscordURL, o.DiscordAPI = api.URL, api.URL+"/bot"
	})
	if before != nil {
		before(m)
	}
	m.Start()
	t.Cleanup(m.Close)
	return m
}

// fakeRun pretends the instance's server started and is ready, as the
// manager sees it (no process: these tests are about what it posts).
func fakeRun(m *Manager, inst *instance, server string) *serverRun {
	now := time.Now()
	run := &serverRun{inst: inst, readyCh: make(chan struct{}), done: make(chan struct{}),
		rec: game.Record{PID: 1, Started: now, Launched: now, World: "Home", Server: server,
			LogPath: filepath.Join(inst.dirs.Logs, inst.id+"-x.log"), Played: game.Played{Mods: map[string]string{}}}}
	m.mu.Lock()
	inst.run = run
	m.mu.Unlock()
	m.handleEvents(run, []game.Event{{Kind: game.KindOnline, Time: now}}, nil)
	return run
}

// endRun pretends the run's server exited, as onExit tells the observers.
func endRun(m *Manager, inst *instance, run *serverRun) {
	m.mu.Lock()
	inst.run = nil
	m.observeLocked(func(o observer) { o.onExit(inst, run, &ExitInfo{At: time.Now(), Expected: true}) })
	m.mu.Unlock()
}

func gameStatus(t *testing.T, m *Manager, id string) GameStatus {
	t.Helper()
	for _, g := range m.Status().Games {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no game %q in the status", id)
	return GameStatus{}
}

func waitPosts(t *testing.T, api *standIn, hook string, n int) []string {
	t.Helper()
	waitFor(t, "posts in webhook "+hook, 20*time.Second, func() bool { return len(api.postsIn(hook)) >= n })
	return api.postsIn(hook)
}

// --- the tests -----------------------------------------------------------------

// A game added with the main channel set, whose Discord settings nobody
// changed (servers.json has none for it), posts its online and stopped
// messages in the main channel, named first so friends can tell the games
// apart, even with Valheim's own posts off.
func TestNewGamePostsInMainChannel(t *testing.T) {
	api := newStandIn(t, time.Now)
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink(1)
		inst = addTestGame(t, m, blockland, nil)
	})
	m.mu.Lock()
	r := m.discordRulesLocked(inst)
	got := m.gameDiscordLocked(inst)
	m.mu.Unlock()
	if !r.posts() || r.link != hookLink(1) || !r.main || !r.stopped || !r.daily || r.password {
		t.Fatalf("a new game's rules = %+v", r)
	}
	if want := (GameDiscordSettings{On: true, Stopped: true, Daily: true, GuildOK: true}); got != want {
		t.Fatalf("its Discord settings = %+v, want %+v", got, want)
	}

	run := fakeRun(m, inst, "Block Party")
	posts := waitPosts(t, api, mainHook, 1)
	want := "Blockland · 🟢 **Block Party** is online\nWorld: Home\n-# In Blockland, choose Join."
	if posts[0] != want {
		t.Fatalf("online post:\n%s\nwant:\n%s", posts[0], want)
	}
	waitFor(t, "the status to say it was posted", 5*time.Second, func() bool {
		d := gameStatus(t, m, inst.id).Discord
		return d != nil && d.Posted != nil && d.Error == ""
	})
	waitFor(t, "the activity entry", 5*time.Second, func() bool {
		acts, _ := m.Activity(0, 0)
		for _, a := range acts {
			if a.Game == inst.id && a.Text == "Posted in Discord that Blockland is online." {
				return true
			}
		}
		return false
	})

	// What onExit queues when it stops.
	m.mu.Lock()
	post, ok := m.stoppedPostLocked(run, stopStopped)
	m.mu.Unlock()
	if !ok {
		t.Fatal("no stopped post")
	}
	m.queueDiscord(post)
	posts = waitPosts(t, api, mainHook, 2)
	if want := "Blockland · 🔴 **Blockland Night** has stopped. A message will be posted here when it's back online."; posts[1] != want {
		t.Fatalf("stopped post:\n%s\nwant:\n%s", posts[1], want)
	}
	if n := len(api.all()); n != 2 {
		t.Fatalf("posts elsewhere: %+v", api.all())
	}
}

// A game with its own channel posts there, without its name first; one
// whose posts are off, or with no channel at all, posts nothing.
func TestGameOwnChannel(t *testing.T) {
	api := newStandIn(t, time.Now)
	var own, off, none *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink(1)
		own = addTestGame(t, m, blockland, &servers.Discord{On: true, Webhook: hookLink(2), Stopped: true, Daily: true, Password: true})
		off = addTestGame(t, m, tundra, &servers.Discord{On: false, Stopped: true, Daily: true})
	})
	fakeRun(m, own, "Block Party")
	posts := waitPosts(t, api, otherHook, 1)
	if !strings.HasPrefix(posts[0], "🟢 **Block Party** is online") {
		t.Fatalf("own channel's post: %q", posts[0])
	}
	m.mu.Lock()
	if link := m.gameLinkLocked(own); link != hookLink(2) {
		t.Errorf("gameLink = %q", link)
	}
	// Turned off: its link is still the main channel (a Discord request is
	// answered there), but it posts nothing and has no Discord status.
	if link := m.gameLinkLocked(off); link != hookLink(1) {
		t.Errorf("gameLink of a game with its posts off = %q", link)
	}
	if st := m.discordStatusLocked(off); st != nil {
		t.Errorf("a game with its posts off has a Discord status: %+v", st)
	}
	m.mu.Unlock()
	fakeRun(m, off, "Frozen")
	time.Sleep(500 * time.Millisecond)
	if n := len(api.postsIn(mainHook)); n != 0 {
		t.Fatalf("a game with its posts off posted: %q", api.postsIn(mainHook))
	}

	// With no main channel, a game without its own has nowhere to post.
	api2 := newStandIn(t, time.Now)
	m2 := gamesManager(t, api2, func(m *Manager) { none = addTestGame(t, m, blockland, nil) })
	m2.mu.Lock()
	r := m2.discordRulesLocked(none)
	st := m2.discordStatusLocked(none)
	m2.mu.Unlock()
	if r.posts() || r.link != "" || st != nil {
		t.Fatalf("no channel: rules %+v, status %+v", r, st)
	}
}

// Valheim keeps its own words wherever it posts: in the main channel, or
// in a channel of its own (servers.json), never with its name first.
func TestValheimPostsAsBefore(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	inst := m.legacy()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings.Discord, m.settings.DiscordWebhook, m.settings.DiscordStopped = true, hookLink(1), false
	r := m.discordRulesLocked(inst)
	if !r.posts() || r.link != hookLink(1) || !r.main || r.stopped || r.words(inst, "x") != "x" {
		t.Fatalf("in the main channel: %+v %q", r, r.words(inst, "x"))
	}
	m.servers.Instances = []servers.Instance{{ID: inst.id, Game: inst.id, Legacy: true,
		Discord: &servers.Discord{Webhook: hookLink(2)}}} // only the webhook counts for Valheim
	r = m.discordRulesLocked(inst)
	if !r.posts() || r.link != hookLink(2) || r.main || r.stopped || r.words(inst, "x") != "x" {
		t.Fatalf("in its own channel: %+v", r)
	}
	m.settings.Discord = false
	if m.discordRulesLocked(inst).posts() {
		t.Fatal("Valheim posts with its posts off")
	}
}

// One channel that Discord can't take posts in for now doesn't hold up
// another channel's posts, and each channel's posts keep their order.
func TestFailingChannelDoesntDelayAnother(t *testing.T) {
	old := discordRetries
	discordRetries = []time.Duration{3 * time.Second, 3 * time.Second}
	t.Cleanup(func() { discordRetries = old })
	api := newStandIn(t, time.Now)
	m := gamesManager(t, api, nil)
	api.setDown(mainHook, true)
	post := func(link, text string) {
		m.queueDiscord(discordPost{link: link, text: func() string { return text }})
	}
	post(hookLink(1), "first in the channel that's down")
	post(hookLink(1), "second in the channel that's down")
	start := time.Now()
	post(hookLink(2), "in the channel that works")
	waitPosts(t, api, otherHook, 1)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the working channel's post took %s", took)
	}
	if n := len(api.postsIn(mainHook)); n != 0 {
		t.Fatalf("posted while down: %q", api.postsIn(mainHook))
	}
	api.setDown(mainHook, false)
	got := waitPosts(t, api, mainHook, 2)
	if got[0] != "first in the channel that's down" || got[1] != "second in the channel that's down" {
		t.Fatalf("order in the channel that was down: %q", got)
	}
	waitFor(t, "nothing left to post", 5*time.Second, func() bool { return m.discordPending.Load() == 0 })
}

// A full queue drops the next post, with a line in the log, and other
// channels still post.
func TestFullQueueDrops(t *testing.T) {
	api := newStandIn(t, time.Now)
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env, func(o *Options) { o.DiscordURL = api.URL })
	// Not started: the posts wait, as they would behind a channel that's down.
	for i := 0; i < discordQueueSize+3; i++ {
		m.queueDiscord(discordPost{link: hookLink(1), text: func() string { return "hi" }})
	}
	if n := m.discordPending.Load(); n != discordQueueSize {
		t.Fatalf("%d waiting, want %d", n, discordQueueSize)
	}
	m.queueDiscord(discordPost{link: hookLink(2), text: func() string { return "other" }})
	m.Start()
	t.Cleanup(m.Close)
	waitPosts(t, api, otherHook, 1)
	waitPosts(t, api, mainHook, discordQueueSize)
}

// Each game posts its own day in its own channel, from its own records,
// and remembers the day in its own records file: Valheim's post is as it
// always was, a game with deaths uses the same words, and a game without
// deaths says who played.
func TestDailyPostsPerGame(t *testing.T) {
	api := newStandIn(t, time.Now)
	var bl, tu *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.Discord, m.settings.DiscordWebhook, m.settings.DiscordDaily = true, hookLink(1), true
		bl = addTestGame(t, m, blockland, &servers.Discord{On: true, Webhook: hookLink(2), Daily: true})
		tu = addTestGame(t, m, tundra, nil)
	})
	// Days far ahead, so the app's own check on the real clock stays out of it.
	day := func(d, h, min int) time.Time { return time.Date(2099, 3, 10+d, h, min, 0, 0, time.Local) }
	ev := func(at time.Time, kind, name string) records.Event {
		return records.Event{T: at.Unix(), Kind: kind, Name: name}
	}
	for _, inst := range []*instance{bl, tu} {
		if !m.ensureRecords(inst) {
			t.Fatalf("%s: records not read", inst.id)
		}
	}
	m.mu.Lock()
	for _, inst := range []*instance{m.legacy(), bl, tu} {
		inst.recs.Posted = day(-1, 0, 0).Format("2006-01-02")
	}
	m.legacy().recs.Runs["server-2099-03-10_17-55-00.log"] = []records.Event{
		ev(day(0, 18, 0), records.Join, "Astrid"), ev(day(0, 18, 5), records.Death, "Astrid"), ev(day(0, 20, 0), records.Leave, "Astrid")}
	bl.recs.Runs["blockland-2099-03-10_17-00-00.log"] = []records.Event{
		ev(day(0, 17, 0), records.Join, "Alex"), ev(day(0, 18, 0), records.Join, "Sam"),
		ev(day(0, 18, 30), records.Leave, "Sam"), ev(day(0, 19, 10), records.Leave, "Alex"),
		ev(day(0, 20, 0), records.Join, "Sam"), ev(day(0, 21, 45), records.Leave, "Sam")}
	tu.recs.Runs["tundra-2099-03-10_17-00-00.log"] = []records.Event{
		ev(day(0, 17, 0), records.Join, "Kai"), ev(day(0, 17, 30), records.Death, "Kai"), ev(day(0, 19, 0), records.Leave, "Kai")}
	m.mu.Unlock()

	m.mu.Lock()
	valheimDay := records.Compute(m.legacy().recs.Runs["server-2099-03-10_17-55-00.log"], day(0, 0, 0), day(1, 0, 0), day(0, 23, 59))
	m.mu.Unlock()

	m.checkDaily(day(0, 23, 59))
	m.checkDaily(day(0, 23, 59).Add(30 * time.Second)) // the next check that minute
	waitFor(t, "the daily posts", 20*time.Second, func() bool { return len(api.all()) >= 3 })
	waitFor(t, "nothing left to post", 5*time.Second, func() bool { return m.discordPending.Load() == 0 })
	main, own := api.postsIn(mainHook), api.postsIn(otherHook)
	wantMain := []string{
		dailyText(valheimDay, "today"), // Valheim's, exactly as always
		"Tundra · 💀 **Kai** died the most today: once\n-# Longest life: Kai, 1 h 30 min",
	}
	// Sam played the most in all; Alex had the longest single session.
	wantOwn := []string{"🎮 **2 players** played Blockland today. Most time: **Sam** (2 h 15 min)\n-# Longest session: Alex, 2 h 10 min"}
	if strings.Join(main, "|") != strings.Join(wantMain, "|") || strings.Join(own, "|") != strings.Join(wantOwn, "|") {
		t.Fatalf("main channel %q\nwant %q\nown channel %q\nwant %q", main, wantMain, own, wantOwn)
	}
	for _, inst := range []*instance{m.legacy(), bl, tu} {
		b, err := os.ReadFile(m.instFile(inst, recordsFile))
		var saved recordsData
		if err != nil || json.Unmarshal(b, &saved) != nil || saved.Posted != "2099-03-10" {
			t.Errorf("%s: posted day in its records file = %q (%v)", inst.id, saved.Posted, err)
		}
	}
	if !hasText(m, "discord", "Posted in Discord who played Blockland today.") ||
		!hasText(m, "discord", "Posted in Discord who died the most in Tundra today.") ||
		!hasText(m, "discord", "Posted in Discord who died the most today.") {
		t.Fatal("an activity entry is missing")
	}

	// The next day only Blockland was played, and its daily post is off.
	m.mu.Lock()
	bl.recs.Runs["blockland-2099-03-11_17-00-00.log"] = []records.Event{
		ev(day(1, 17, 0), records.Join, "Alex"), ev(day(1, 18, 0), records.Leave, "Alex")}
	m.servers.Instance(bl.id).Discord.Daily = false
	m.mu.Unlock()
	m.checkDaily(day(2, 9, 0)) // the morning after: yesterday's
	time.Sleep(300 * time.Millisecond)
	if n := len(api.all()); n != 3 {
		t.Fatalf("posted with nobody playing or the post off: %+v", api.all())
	}
}

func TestPlayedText(t *testing.T) {
	tx := game.Texts{Players: "players", Player: "player"}
	for _, c := range []struct {
		sum  records.Summary
		when string
		want string
	}{
		{records.Summary{Players: []records.Player{{Name: "Alex"}, {Name: "Sam"}, {Name: "Jo"}},
			Records: []records.Record{{ID: "playTime", Names: []string{"Alex"}, Value: 130 * 60},
				{ID: "longestSession", Names: []string{"Alex"}, Value: 130 * 60}}},
			"today", "🎮 **3 players** played Terraria today. Most time: **Alex** (2 h 10 min)"},
		{records.Summary{Players: []records.Player{{Name: "_Alex_"}},
			Records: []records.Record{{ID: "playTime", Names: []string{"_Alex_"}, Value: 90 * 60},
				{ID: "longestSession", Names: []string{"_Alex_"}, Value: 50 * 60}}},
			"yesterday", "🎮 **1 player** played Terraria yesterday. Most time: **\\_Alex\\_** (1 h 30 min)\n-# Longest session: \\_Alex\\_, 50 min"},
		{records.Summary{Players: []records.Player{{Name: "Alex"}, {Name: "Sam"}}},
			"today", "🎮 **2 players** played Terraria today."},
	} {
		if got := playedTextFor("Terraria", tx, c.sum, c.when); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

// After auto-stop the stopped post says how long nobody played, from the
// minutes it waited (30, 60 and 120 here), and names the game's slash
// command only when friends can use it: the game is starred and the waker
// is set up (spec 4.5).
func TestAutoStopTexts(t *testing.T) {
	for minutes, want := range map[int]string{
		30:  "🔴 **Block\\_Party** has stopped: nobody played for 30 minutes. Start it again from the control panel.",
		60:  "🔴 **Block\\_Party** has stopped: nobody played for an hour. Start it again from the control panel.",
		120: "🔴 **Block\\_Party** has stopped: nobody played for 2 hours. Start it again from the control panel.",
	} {
		if got := idleStoppedText("Block_Party", minutes, ""); got != want {
			t.Errorf("%d minutes:\n%s\nwant\n%s", minutes, got, want)
		}
	}

	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	inst := addTestGame(t, m, blockland, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings.DiscordWebhook = hookLink(1)
	run := &serverRun{inst: inst, rec: game.Record{Announced: "online"}, autoStopped: true, autoReason: stopIdle}
	text := func() string {
		t.Helper()
		p, ok := m.stoppedPostLocked(run, run.autoReason)
		if !ok {
			return ""
		}
		return p.text()
	}
	for minutes, want := range map[int]string{
		30:  "Blockland · 🔴 **Blockland Night** has stopped: nobody played for 30 minutes. Start it again from the control panel.",
		60:  "Blockland · 🔴 **Blockland Night** has stopped: nobody played for an hour. Start it again from the control panel.",
		120: "Blockland · 🔴 **Blockland Night** has stopped: nobody played for 2 hours. Start it again from the control panel.",
	} {
		run.autoMinutes = minutes
		if got := text(); got != want {
			t.Errorf("%d minutes:\n%s\nwant\n%s", minutes, got, want)
		}
	}
	// Starred, and the waker is on: friends can start it again from Discord.
	m.servers.App.Stars = []string{inst.id}
	m.settings.DiscordWake = true
	run.autoMinutes = 60
	if got, want := text(), "Blockland · 🔴 **Blockland Night** has stopped: nobody played for an hour. Start it again with /blockland or from the control panel."; got != want {
		t.Errorf("starred:\n%s\nwant\n%s", got, want)
	}
	m.servers.App.DisabledGames = []string{inst.id} // turned off: no command
	if got := text(); strings.Contains(got, "/blockland") {
		t.Errorf("a turned-off game's command is named: %s", got)
	}
	// The "still playing?" rule's stop has no stopped post: its "Turning …
	// off" line was it.
	run.autoReason = stopAsked
	if got := text(); got != "" {
		t.Errorf("the ask rule's stop made a stopped post: %q", got)
	}
}

// A new main channel takes the posts of every game without a channel of
// its own, and a game that's online posts there again at once.
func TestSetMainDiscord(t *testing.T) {
	api := newStandIn(t, time.Now)
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink(1)
		inst = addTestGame(t, m, blockland, nil)
	})
	fakeRun(m, inst, "Block Party")
	waitPosts(t, api, mainHook, 1)

	var fe config.FieldErrors
	if err := m.SetMainDiscord("https://discord.com/channels/1/2"); !errors.As(err, &fe) || !strings.Contains(fe["discordWebhook"], "link to the channel") {
		t.Fatalf("a channel link: %v", err)
	}
	if err := m.SetMainDiscord(" " + hookLink(3) + "\n"); err != nil {
		t.Fatal(err)
	}
	posts := waitPosts(t, api, "3", 1)
	if !strings.HasPrefix(posts[0], "Blockland · 🟢 **Block Party** is online") {
		t.Fatalf("in the new main channel: %q", posts)
	}
	if s := m.Settings(); s.DiscordWebhook != hookLink(3) || s.Discord {
		t.Fatalf("settings: webhook %q, Valheim's posts %v", s.DiscordWebhook, s.Discord)
	}

	// Removing it: Valheim's posts go off with it, and nothing is posted.
	s := m.Settings()
	s.Discord = true
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMainDiscord(""); err != nil {
		t.Fatal(err)
	}
	if s := m.Settings(); s.DiscordWebhook != "" || s.Discord {
		t.Fatalf("after removing it: webhook %q, Valheim's posts %v", s.DiscordWebhook, s.Discord)
	}
	m.mu.Lock()
	posts2 := m.discordRulesLocked(inst).posts()
	m.mu.Unlock()
	if posts2 {
		t.Fatal("a game still posts with no channel")
	}
}

// The Test button posts in the link given, worded for the game; the legacy
// body (no game) keeps Valheim's message.
func TestTestGameDiscord(t *testing.T) {
	api := newStandIn(t, time.Now)
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink(1)
		m.settings.ServerName = "Viking Night"
		inst = addTestGame(t, m, blockland, nil)
	})
	if err := m.TestGameDiscord(hookLink(1), inst.id); err != nil {
		t.Fatal(err)
	}
	if err := m.TestGameDiscord(hookLink(2), inst.id); err != nil {
		t.Fatal(err)
	}
	if err := m.TestGameDiscord(hookLink(2), ""); err != nil {
		t.Fatal(err)
	}
	main, own := api.postsIn(mainHook), api.postsIn(otherHook)
	if len(main) != 1 || main[0] != "Blockland · ✅ **Blockland Night** can post here. Each time it starts, how to join will appear in this channel." {
		t.Fatalf("in the main channel: %q", main)
	}
	if len(own) != 2 || own[0] != "✅ **Blockland Night** can post here. Each time it starts, how to join will appear in this channel." ||
		!strings.HasPrefix(own[1], "✅ **Viking Night** can post here. Each time the server starts,") {
		t.Fatalf("in its own channel: %q", own)
	}
	var fe config.FieldErrors
	if err := m.TestGameDiscord("https://discord.gg/abc", inst.id); !errors.As(err, &fe) || fe["discordWebhook"] == "" {
		t.Fatalf("an invite link: %v", err)
	}
	if err := m.TestGameDiscord(hookLink(1), "nosuchgame"); err == nil {
		t.Fatal("a game that isn't there was tested")
	}
}

// A game's own channel in another Discord server is flagged on its
// Settings tab, once Discord has said where each channel is.
func TestGuildOK(t *testing.T) {
	api := newStandIn(t, time.Now)
	api.mu.Lock()
	api.guilds["2"] = "g2"
	api.mu.Unlock()
	var other, same *instance
	m := gamesManager(t, api, func(m *Manager) {
		m.settings.DiscordWebhook = hookLink(1)
		other = addTestGame(t, m, blockland, &servers.Discord{On: true, Webhook: hookLink(2)})
		same = addTestGame(t, m, tundra, &servers.Discord{On: true, Webhook: hookLink(3)})
	})
	waitFor(t, "Discord to say where the channels are", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.gameDiscordLocked(other).GuildOK
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.gameDiscordLocked(same); !d.GuildOK || d.Webhook != hookLink(3) {
		t.Fatalf("a channel in the same Discord server: %+v", d)
	}
}

// A change to a game's Discord settings is checked before it's saved;
// Valheim's on/off is settings.json's, as the old Settings page saves it.
func TestSetGameDiscord(t *testing.T) {
	api := newStandIn(t, time.Now)
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) { inst = addTestGame(t, m, blockland, nil) })
	bad := "https://discord.gg/abc"
	var fe game.FieldErrors
	if err := m.setGameDiscord(inst, GameDiscordChange{Webhook: &bad}); !errors.As(err, &fe) || !strings.Contains(fe["discord.webhook"], "invite link") {
		t.Fatalf("an invite link: %v", err)
	}
	on := true
	if err := m.setGameDiscord(m.legacy(), GameDiscordChange{On: &on}); !errors.As(err, &fe) || !strings.Contains(fe["discord.on"], "main Discord channel") {
		t.Fatalf("Valheim's posts on with no main channel: %v", err)
	}
	if err := m.SetMainDiscord(hookLink(1)); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := m.setGameDiscord(m.legacy(), GameDiscordChange{On: &on, Daily: &off}); err != nil {
		t.Fatal(err)
	}
	if s := m.Settings(); !s.Discord || s.DiscordDaily || !s.DiscordStopped {
		t.Fatalf("settings.json: discord %v, daily %v, stopped %v", s.Discord, s.DiscordDaily, s.DiscordStopped)
	}
	m.mu.Lock()
	got := m.gameDiscordLocked(m.legacy())
	m.mu.Unlock()
	if !got.On || got.Daily || !got.Stopped || got.Webhook != "" {
		t.Fatalf("Valheim's Discord settings: %+v", got)
	}
}

// Each game's records come from its own runs.
func TestRecordsGame(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	inst := addTestGame(t, m, blockland, nil)
	now := time.Now()
	m.ensureRecords(inst)
	m.mu.Lock()
	inst.recs.Runs["blockland-1.log"] = []records.Event{
		{T: now.Add(-2 * time.Hour).Unix(), Kind: records.Join, Name: "Alex"},
		{T: now.Add(-time.Hour).Unix(), Kind: records.Leave, Name: "Alex"}}
	m.mu.Unlock()
	sum, err := m.RecordsGame(inst.id, "all")
	if err != nil || len(sum.Players) != 1 || sum.Players[0].Name != "Alex" {
		t.Fatalf("records = %+v, %v", sum, err)
	}
	if sum, err := m.RecordsGame(m.legacyID, "week"); err != nil || len(sum.Players) != 0 {
		t.Fatalf("Valheim's records = %+v, %v", sum, err)
	}
	if _, err := m.RecordsGame(inst.id, "century"); err == nil {
		t.Fatal("an unknown period was accepted")
	}
	if _, err := m.RecordsGame("nosuchgame", "all"); err == nil {
		t.Fatal("a game that isn't there has records")
	}
}

// A game's records file is read before anything is saved over it: New
// doesn't read it (I5), and the first daily check, or a run that ends
// first, adds to what the file has instead of replacing it. A file that is
// there but can't be read is left alone until it can be.
func TestRecordsReadBeforeSaving(t *testing.T) {
	env := &testEnv{data: t.TempDir()}
	m := newTestManager(t, env.data, env)
	bl := addTestGame(t, m, blockland, nil)
	tu := addTestGame(t, m, tundra, nil)
	sf := addTestGame(t, m, seafarer, nil)
	put := func(inst *instance, d recordsData) {
		t.Helper()
		b, _ := json.Marshal(d)
		path := m.instFile(inst, recordsFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved := func(inst *instance) recordsData {
		t.Helper()
		var d recordsData
		b, err := os.ReadFile(m.instFile(inst, recordsFile))
		if err != nil || json.Unmarshal(b, &d) != nil {
			t.Fatalf("%s: records file: %v %s", inst.id, err, b)
		}
		return d
	}
	oldRun := []records.Event{{T: 100, Kind: records.Join, Name: "Alex"}, {T: 200, Kind: records.Leave, Name: "Alex"}}
	put(bl, recordsData{Runs: map[string][]records.Event{"blockland-old.log": oldRun}, Posted: "2026-01-01"})
	put(tu, recordsData{Runs: map[string][]records.Event{"tundra-old.log": oldRun}, Posted: "2026-01-01"})
	if err := os.MkdirAll(m.instFile(sf, recordsFile), 0o755); err != nil { // in the way of the file
		t.Fatal(err)
	}

	// A Tundra run ends before anything read its records: the file keeps
	// its run and the day it posted, and gains the new run.
	run := &serverRun{inst: tu, rec: game.Record{LogPath: filepath.Join(tu.dirs.Logs, "tundra-new.log")},
		events: []records.Event{{T: 300, Kind: records.Join, Name: "Kai"}, {T: 400, Kind: records.Leave, Name: "Kai"}}}
	m.mu.Lock()
	archived := m.archiveRunLocked(run)
	m.mu.Unlock()
	if !archived {
		t.Fatal("the run wasn't kept")
	}
	m.saveRecordsFor(tu)
	if d := saved(tu); len(d.Runs["tundra-old.log"]) != 2 || len(d.Runs["tundra-new.log"]) != 2 || d.Posted != "2026-01-01" {
		t.Fatalf("Tundra's records after a run ended: %+v", d)
	}

	// The daily check is the first to need Blockland's: it reads the file,
	// then saves the day it got to, with the old run still there.
	m.checkDaily(time.Date(2026, 11, 27, 9, 0, 0, 0, time.Local))
	if d := saved(bl); len(d.Runs["blockland-old.log"]) != 2 || d.Posted != "2026-11-26" {
		t.Fatalf("Blockland's records after the daily check: %+v", d)
	}

	// Seafarer's can't be read: nothing counts as read, and the daily check
	// doesn't note a day it couldn't compare.
	if m.ensureRecords(sf) {
		t.Fatal("records that couldn't be read count as read")
	}
	m.mu.Lock()
	posted := sf.recs.Posted
	m.mu.Unlock()
	if posted != "" {
		t.Fatalf("Seafarer's day posted = %q", posted)
	}
	if fi, err := os.Stat(m.instFile(sf, recordsFile)); err != nil || !fi.IsDir() {
		t.Fatalf("what was in the file's place was changed: %v", err)
	}
}

// What a game's players last played with is read once the app has started
// (New reads only Valheim's), so its reminders compare with it.
func TestPlayedReadAfterStart(t *testing.T) {
	api := newStandIn(t, time.Now)
	var inst *instance
	m := gamesManager(t, api, func(m *Manager) {
		inst = addTestGame(t, m, blockland, nil)
		b, _ := json.Marshal(played{Game: "1.4.5", Mods: map[string]string{}, Password: hashPassword("secret")})
		path := m.instFile(inst, "lastplayed.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if inst.lastPlayed != nil {
			t.Fatal("read before the app started")
		}
	})
	waitFor(t, "lastplayed.json to be read", 10*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return inst.lastPlayed != nil && inst.lastPlayed.Game == "1.4.5"
	})
}

// Valheim's own channel and its posts turned on in one save: servers.json
// (its channel) is written before settings.json (its posts), so the online
// message goes out once, in the channel just chosen; a refused servers.json
// leaves settings.json as it was, and a refused settings.json puts the
// channel back.
func TestSetGameDiscordLegacyChannel(t *testing.T) {
	api := newStandIn(t, time.Now)
	env := setup(t)
	m := env.m
	m.opt.DiscordURL, m.opt.DiscordAPI = api.URL, api.URL+"/bot"
	install(t, m)
	s := m.Settings()
	s.DiscordWebhook = hookLink(1) // the main channel; Valheim's posts are off
	if err := m.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	legacy := m.legacy()
	var refuse error
	m.mu.Lock()
	m.servers.Instances = []servers.Instance{{ID: legacy.id, Game: legacy.id, Legacy: true, Added: time.Now()}}
	m.mu.Unlock()
	m.disc.saveServers = func(change func(*servers.File) error) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if refuse != nil {
			return refuse
		}
		return change(&m.servers)
	}
	code := startOnline(t, m)
	on, own := true, hookLink(2)
	ownWebhook := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.gameDiscordLocked(legacy).Webhook
	}

	// servers.json refused: nothing changes, nothing is posted.
	m.mu.Lock()
	refuse = errors.New("The list of games can't be changed now.")
	m.mu.Unlock()
	if err := m.setGameDiscord(legacy, GameDiscordChange{On: &on, Webhook: &own}); err == nil || err.Error() != "The list of games can't be changed now." {
		t.Fatalf("refused: %v", err)
	}
	if m.Settings().Discord || ownWebhook() != "" {
		t.Fatalf("half saved: posts on %v, own channel %q", m.Settings().Discord, ownWebhook())
	}

	// settings.json refused (something is in its way): the channel is put
	// back.
	m.mu.Lock()
	refuse = nil
	settingsPath := m.paths.Settings
	m.mu.Unlock()
	keep, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settingsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.setGameDiscord(legacy, GameDiscordChange{On: &on, Webhook: &own}); err == nil {
		t.Fatal("saved with settings.json in the way")
	}
	if m.Settings().Discord || ownWebhook() != "" {
		t.Fatalf("half saved: posts on %v, own channel %q", m.Settings().Discord, ownWebhook())
	}
	if err := os.Remove(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, keep, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if posts := api.all(); len(posts) != 0 {
		t.Fatalf("posted after refused saves: %+v", posts)
	}

	// Both saved: one online post, in its own channel only.
	if err := m.setGameDiscord(legacy, GameDiscordChange{On: &on, Webhook: &own}); err != nil {
		t.Fatal(err)
	}
	waitPosts(t, api, otherHook, 1)
	waitFor(t, "nothing left to post", 5*time.Second, func() bool { return m.discordPending.Load() == 0 })
	time.Sleep(300 * time.Millisecond)
	if posts := api.postsIn(otherHook); len(posts) != 1 || !strings.Contains(posts[0], "Join code: **"+code+"**") {
		t.Fatalf("in its own channel: %q", posts)
	}
	if main := api.postsIn(mainHook); len(main) != 0 {
		t.Fatalf("in the main channel: %q", main)
	}
	if !m.Settings().Discord || ownWebhook() != own {
		t.Fatalf("saved: posts on %v, own channel %q", m.Settings().Discord, ownWebhook())
	}

	// Moved back to the main channel with its posts already on: posted
	// again there, once.
	none := ""
	if err := m.setGameDiscord(legacy, GameDiscordChange{On: &on, Webhook: &none}); err != nil {
		t.Fatal(err)
	}
	waitPosts(t, api, mainHook, 1)
	waitFor(t, "nothing left to post", 5*time.Second, func() bool { return m.discordPending.Load() == 0 })
	time.Sleep(300 * time.Millisecond)
	if n, o := len(api.postsIn(mainHook)), len(api.postsIn(otherHook)); n != 1 || o != 1 {
		t.Fatalf("after moving back: %d in the main channel, %d in its own", n, o)
	}
}
