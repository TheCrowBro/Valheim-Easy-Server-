package windrose

import (
	"encoding/json"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata from what the code does now")

// Everything every adapter must do, against the stand-in (cmd/fakeserver's
// windrose personality): install, the first start that makes the settings
// file and the world, online, the scripted join and leave, Join with the
// invite code, the stop by Ctrl+C, a second start on the same world, a
// takeover whose stop still works, and a backup that restores.
func TestConformance(t *testing.T) {
	gametest.Conformance(t, Game{}, gametest.Options{})
}

// The registry knows Windrose once the package is imported (the lead adds
// internal/games/all/windrose.go after the R2 cut).
func TestRegistered(t *testing.T) {
	g, ok := game.Get(ID)
	if !ok {
		t.Fatal("windrose isn't registered")
	}
	in := g.Info()
	if in.Command != "windrose" || in.Stage != game.Preview || in.Legacy || in.Order != 100 {
		t.Errorf("Info: command %q, stage %q, legacy %v, order %d", in.Command, in.Stage, in.Legacy, in.Order)
	}
	if in.OS["windows"] != game.Runs || in.OS["darwin"] == game.Runs || in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("Info.OS = %v, NeedsText = %v: Windows only, \"Needs Windows\" on a Mac", in.OS, in.NeedsText)
	}
	if len(in.Ports) != 0 || in.Password.Use != "optional" || in.Presence != game.ByName || in.Deaths || in.Import || in.Lists || in.Mods {
		t.Errorf("Info: ports %v, password %+v, presence %q, deaths %v, import %v", in.Ports, in.Password, in.Presence, in.Deaths, in.Import)
	}
	if in.MemoryMB != 12288 || in.DiskMB != 35000 || in.DownloadMB < 3000 || in.DownloadMB > 3200 || in.MaxPlayers != 4 {
		t.Errorf("Info: memory %d MB, disk %d MB, download %d MB, players %d", in.MemoryMB, in.DiskMB, in.DownloadMB, in.MaxPlayers)
	}
}

func testEnv(t *testing.T) game.Env {
	root := filepath.Join(t.TempDir(), "games", ID)
	return game.Env{Instance: ID, Dirs: game.Dirs{
		Root: root, Server: filepath.Join(root, "server"), Saves: filepath.Join(root, "saves"),
		Backups: filepath.Join(root, "backups"), Logs: filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"), Staging: filepath.Join(root, "uploads"),
	}}
}

// defaultSaved is what Add a game saves: the schema's defaults.
func defaultSaved(world string) game.Saved {
	sv := game.Saved{Server: game.Values{}, World: world, Worlds: map[string]game.Values{world: {}}, State: game.Values{}}
	for _, f := range (&server{}).Schema() {
		if f.Scope == game.ServerScope && f.Default != "" {
			sv.Server[f.Key] = f.Default
		}
	}
	return sv
}

// installed makes an adapter whose server program is there (an empty
// file is enough for Plan), with a ServerDescription.json as the server
// made it at its first start, so Plan doesn't start anything.
func installed(t *testing.T, description string) *server {
	t.Helper()
	s := Game{}.New(testEnv(t)).(*server)
	if err := os.MkdirAll(filepath.Dir(s.Program()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Program(), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if description != "" {
		copyFile(t, filepath.Join("testdata", description), s.descriptionPath())
	}
	return s
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeWorld puts a world on disk as the server lays it out: its live
// folder with WorldDescription.json, and its backups with a _Latest one.
func makeWorld(t *testing.T, s *server, id, worldName string) {
	t.Helper()
	live := filepath.Join(s.savesDir(), liveRoot, "0.10.0", worldsFolder, id)
	back := filepath.Join(s.savesDir(), backupRoot, "0.10.0", worldsFolder, id)
	for _, d := range []string{live, back} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	desc, _ := json.Marshal(map[string]any{"Version": 1, "WorldDescription": map[string]any{"IslandId": id, "WorldName": worldName, "WorldPresetType": "Medium"}})
	write := func(p string, b []byte) {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(live, worldFile), desc)
	write(filepath.Join(live, "CURRENT"), []byte("MANIFEST-000001\n"))
	write(filepath.Join(back, id+"_0.10.0.9.32_2026.11.26-20.00.00.000_Latest.zip"), []byte("PK\x05\x06"+strings.Repeat("\x00", 18)))
}

// fixedNetwork makes the P2pProxyAddress check see a computer whose
// address is 192.168.1.20.
func fixedNetwork(t *testing.T) {
	t.Helper()
	oldLocal, oldOut := localIPs, outboundIP
	t.Cleanup(func() { localIPs, outboundIP = oldLocal, oldOut })
	localIPs = func() []net.IP { return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20")} }
	outboundIP = func() net.IP { return net.ParseIP("192.168.1.20") }
}

// fixedIDs makes new world ids predictable.
func fixedIDs(t *testing.T, ids ...string) {
	t.Helper()
	old := newWorldID
	t.Cleanup(func() { newWorldID = old })
	newWorldID = func() string {
		id := ids[0]
		if len(ids) > 1 {
			ids = ids[1:]
		}
		return id
	}
}

// notRunning makes the world store see a stopped server.
func notRunning(t *testing.T) {
	t.Helper()
	old := running
	t.Cleanup(func() { running = old })
	running = func(string) bool { return false }
}

// waitUntil polls cond.
func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
