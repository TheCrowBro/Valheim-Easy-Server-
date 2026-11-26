package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The corekeeper personality stands in for the Core Keeper dedicated
// server (internal/games/corekeeper), as the real 1.3.0.7 server behaves
// (that package's FACTS.md and testdata hold its real log). The app starts
// it as the server's own program:
//
//	CoreKeeperServer.exe -batchmode -logfile LOG -datapath DIR -world 0
//	    -worldname NAME -maxplayers 8 -gameid ID [-worldmode Normal -worldseed SEED]
//
// Match knows it by its program's name, which no other game's server has.
//
// Like the real server it writes its lines only to -logfile (Unity's
// memory settings go to standard output); keeps the world as
// <datapath>/worlds/<world>.world.gzip, written only when it saves, with
// the save before kept as .pugbackup; writes ServerConfig.json, Admins.json
// and PlayerBans.json into the data folder at start, and GameInfo.txt and
// GameID.txt beside its program once its session is up; says
// "Started session with info: <Game ID>" when friends can join; opens no
// port (the real one goes through Steam's relay); and on Ctrl+C (SIGINT;
// on Windows the app's Ctrl+C, since the stand-in is a console program)
// saves the world and quits, with the real one's lines. It pauses
// ("timescale = 0") while nobody is on and autosaves only while someone is.

func init() {
	register(&Personality{
		ID:      "corekeeper",
		Version: "1.3.0.7",
		MemMB:   64,
		Match:   func(c Call) bool { return c.Program == "corekeeperserver" },
		Run:     runCoreKeeper,
	})
}

// coreKeeper is one run of the stand-in.
type coreKeeper struct {
	s         *Server
	worldPath string // <datapath>/worlds/<world>.world.gzip
	saves     int    // how often this world was saved, kept in the file
	stopping  bool
	accounts  map[string]string // name → made-up Steam account
	slots     int
}

// ckGameIDChars are the characters the real server takes in a Game ID.
const ckGameIDChars = "123456789ABCDEFGHIJKLMNPQRSTUVWXZabcdefghijklmnopqrstuvwz"

func ckValidGameID(id string) bool {
	if len(id) < 15 || len(id) > 28 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune(ckGameIDChars, r) {
			return false
		}
	}
	return true
}

// ckConfig is the real server's ServerConfig.json.
type ckConfig struct {
	GameID           string `json:"gameId"`
	Password         string `json:"password"`
	World            int    `json:"world"`
	WorldName        string `json:"worldName"`
	WorldSeed        string `json:"worldSeed"`
	HashedWorldSeed  uint32 `json:"hashedWorldSeed"`
	MaxNumberPlayers int    `json:"maxNumberPlayers"`
	MaxPackets       int    `json:"maxNumberPacketsSentPerFrame"`
	NetworkSendRate  int    `json:"networkSendRate"`
	WorldMode        int    `json:"worldMode"`
	SeasonOverride   int    `json:"seasonOverride"`
}

var ckModes = map[string]int{"normal": 0, "hard": 1, "creative": 2, "casual": 4}

func runCoreKeeper(s *Server) {
	a := s.Args()
	if lf := a.String("logfile", ""); lf != "" && lf != "-" {
		s.LogTo(lf)
	}
	ck := &coreKeeper{s: s, accounts: map[string]string{}}
	data := a.String("datapath", filepath.Join(s.call.Dir, "DedicatedServer"))
	world := a.Int("world", 0)
	if world < 0 || world > 29 {
		s.Logf("World ID needs to be between %d and %d, inclusive", 0, 29)
		world = 0
	}

	s.Log("--- Startup info ---")
	s.Log("Time (UTC): " + time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	s.Log("Game version: " + s.Version() + "-fake")
	s.Log("Unity version: 6000.0.59f2")
	s.Log("--- END Startup info ---")
	for i, arg := range append([]string{"./CoreKeeperServer"}, a.List()...) {
		s.Logf("CommandLineArgs.args[%d]: %s", i, arg)
	}
	s.Log("Initializing Steamworks with AppID 1621690")
	s.Log("Steam API initialized")
	s.Log("Logged in with userid:1866682385")

	// The settings: what the data folder's ServerConfig.json says, with
	// the command line on top, written back as the real server does.
	cfg := ckConfig{MaxNumberPlayers: 100, MaxPackets: 1, NetworkSendRate: 20, SeasonOverride: -1}
	cfgPath := filepath.Join(data, "ServerConfig.json")
	if b, err := os.ReadFile(cfgPath); err == nil {
		json.Unmarshal(b, &cfg)
	}
	cfg.World = world
	if v := a.String("worldname", ""); v != "" {
		cfg.WorldName = v
	}
	if a.Has("maxplayers") {
		cfg.MaxNumberPlayers = a.Int("maxplayers", cfg.MaxNumberPlayers)
	}
	if v, ok := ckModes[strings.ToLower(a.String("worldmode", ""))]; ok {
		cfg.WorldMode = v
	}
	if v := a.String("worldseed", ""); v != "" {
		cfg.WorldSeed = v
	}
	info := ""
	switch id := a.String("gameid", ""); {
	case ckValidGameID(id):
		cfg.GameID = id
	case ckValidGameID(cfg.GameID):
	default:
		info = "No GameID found. Creating new GameID for the session.\n\n"
		var b strings.Builder
		for range 15 {
			b.WriteByte(ckGameIDChars[rand.IntN(len(ckGameIDChars))])
		}
		cfg.GameID = b.String()
	}
	ck.slots = cfg.MaxNumberPlayers
	os.MkdirAll(data, 0o755)
	ckWriteJSON(cfgPath, cfg)
	ckWriteJSON(filepath.Join(data, "Admins.json"), map[string][]string{"adminList": {}})
	ckWriteJSON(filepath.Join(data, "PlayerBans.json"), map[string][]string{"banList": {}})

	s.Log("Loading data blocks...")
	s.Sleep(8 * time.Second)
	s.Log("Data blocks finished loading from addressables (7.64s, 13716 blocks in 28 batches)")
	s.Log("Listening on SteamID userid:90298347780526097")
	s.Log("ConvertAuthoringDataRoutine complete. Process was successful.")
	s.Log(cfg.GameID)

	s.OnStep(ck.step)
	s.OnStop(ck.quit)
	s.OnCrash(func() {
		// Unity's own words when the player dies by itself.
		s.Log("Crash!!!")
		s.Log("SymInit: Symbol-SearchPath: '.', symOptions: 534, UserName: 'owner'")
	})
	s.Every(5*time.Minute, ck.autosave)
	s.Ready("Started session with info: " + cfg.GameID)

	// GameInfo.txt and GameID.txt go beside the program, as the real one
	// writes them (its "GameInfo path:" line).
	s.Log("GameInfo path: " + filepath.Join(s.call.Dir, "CoreKeeperServer_Data", "..", "GameInfo.txt"))
	os.WriteFile(filepath.Join(s.call.Dir, "GameInfo.txt"), []byte(info+"GameID: "+cfg.GameID), 0o644)
	s.Log("GameID path: " + filepath.Join(s.call.Dir, "CoreKeeperServer_Data", "..", "GameID.txt"))
	os.WriteFile(filepath.Join(s.call.Dir, "GameID.txt"), []byte(cfg.GameID), 0o644)

	ck.worldPath = filepath.Join(data, "worlds", strconv.Itoa(world)+".world.gzip")
	if saves, size, ok := ckReadWorld(ck.worldPath); ok {
		ck.saves = saves
		s.Logf("Decompressed brotli compressed world file: %d bytes -> %d bytes", size, size*11)
		s.Log("Deserialize world finishied")
		s.Log("successfully loaded world file into SerializeWorld")
		s.Logf("found %d chunks in serialized world", 14+saves)
	} else {
		s.Log("no existing save file, starting with empty world")
		s.Logf("starting a new world : %016x%016x", rand.Uint64(), rand.Uint64())
		if cfg.WorldSeed == "" {
			cfg.WorldSeed = strconv.FormatUint(rand.Uint64()%1e12, 10)
			s.Logf("No seed string provided, using random string: %s hashed to %d", cfg.WorldSeed, ckHash(cfg.WorldSeed))
		} else {
			s.Logf("Hashed string seed %s -> %d", cfg.WorldSeed, ckHash(cfg.WorldSeed))
		}
		cfg.HashedWorldSeed = ckHash(cfg.WorldSeed)
		ckWriteJSON(cfgPath, cfg)
		s.Logf("Using seed %d", cfg.HashedWorldSeed)
		s.Log("Starting new world with generation type FullRelease")
	}
	s.Log("timescale = 0")
}

func ckHash(seed string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(seed))
	return h.Sum32()
}

// ckReadWorld reads how often a world was saved and the file's size;
// false when there is no world file.
func ckReadWorld(path string) (saves int, size int, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return 0, len(b), true
	}
	content, _ := io.ReadAll(zr)
	first, _, _ := strings.Cut(string(content), "\n")
	saves, _ = strconv.Atoi(strings.TrimPrefix(first, "saves="))
	return saves, len(b), true
}

// save writes the world, a little bigger each time, keeping the one
// before as .pugbackup, as the real server does.
func (ck *coreKeeper) save() {
	ck.saves++
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	fmt.Fprintf(zw, "saves=%d\n", ck.saves)
	// Random bytes, so the compressed file grows with every save.
	pad := make([]byte, 400+40*ck.saves)
	for i := range pad {
		pad[i] = byte(rand.IntN(256))
	}
	zw.Write(pad)
	zw.Close()
	os.MkdirAll(filepath.Dir(ck.worldPath), 0o755)
	if _, err := os.Stat(ck.worldPath); err == nil {
		os.Rename(ck.worldPath, ck.worldPath+".pugbackup")
	}
	tmp := ck.worldPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err == nil {
		os.Rename(tmp, ck.worldPath)
	}
}

// autosave runs every 5 minutes on the game's clock while someone is on;
// with nobody on the world is paused and nothing changes.
func (ck *coreKeeper) autosave() {
	if ck.stopping || len(ck.s.Players()) == 0 {
		return
	}
	ck.save()
}

// quit is the real server's answer to a quit request: it holds the quit
// until the world is saved, then quits.
func (ck *coreKeeper) quit() {
	s := ck.s
	s.Log("Got quit request")
	if ck.stopping {
		s.Log("Save is still pending")
		s.Log("Exit blocked by ECSManager")
		s.Log("Quit blocked")
		return
	}
	ck.stopping = true
	s.Log("Exit blocked by ECSManager")
	s.Log("Quit blocked")
	s.Log("timescale = 1")
	s.Log("Read cartography table map with 0 parts")
	s.Sleep(2 * time.Second)
	s.Log("Allocating a new file write buffer of size 8.000MB for WorldSave. Total buffer size for this file including pool is 8.000MB")
	s.Log("timescale = 0")
	s.Log("Got quit request")
	s.Log("Running quit handlers")
	ck.save()
	s.Log("PlatformManager was destroyed")
	s.Log("Input System module state changed to: Shutdown.")
	s.Exit(0)
}

// account is a made-up Steam account number for a name, the same every
// time.
func (ck *coreKeeper) account(name string) string {
	if a, ok := ck.accounts[name]; ok {
		return a
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	a := strconv.FormatInt(76561198000000000+int64(h.Sum32()%100000000), 10)
	ck.accounts[name] = a
	return a
}

func (ck *coreKeeper) step(st Step) bool {
	s := ck.s
	acc := ck.account(st.Name)
	switch st.Verb {
	case "join":
		if len(s.Players()) >= ck.slots {
			s.Logf("Rejecting player with userid:%s since player limit has been reached", acc)
			return true
		}
		if !s.Join(st.Name) {
			return true
		}
		s.Logf("Accepted connection from %s with result OK awaiting authentication", acc)
		s.Log("Connected to userid:" + acc)
		s.Log("Successful authentication from userid: " + acc)
		if len(s.Players()) == 1 {
			s.Log("timescale = 1")
		}
		s.Log("spawning player with character data size 16682")
		s.Logf("[userid:%s] player %s connected islocalplayer=False", acc, st.Name)
		s.Logf("[userid:%s] is using new name %s", acc, st.Name)
		s.Log("Started game for new connection")
	case "leave":
		if s.Leave(st.Name) {
			s.Logf("Disconnected from userid:%s with reason App_Min", acc)
			s.Log("check connection state")
			s.Log("player disconnect")
			if len(s.Players()) == 0 {
				s.Log("timescale = 0")
			}
		}
	case "deny":
		// Turned away: the server is full.
		s.Logf("Rejecting player with userid:%s since player limit has been reached", acc)
	case "save":
		ck.save()
	default:
		return false
	}
	return true
}

// ckWriteJSON writes v as the real server does: indented with four
// spaces, keeping the file before as .pugbackup.
func ckWriteJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err == nil {
		os.Rename(path, path+".pugbackup")
	}
	os.WriteFile(path, b, 0o644)
}
