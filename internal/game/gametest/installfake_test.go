package gametest

// install.Fake's tests live here, with the rest of stream F's work
// (internal/install's other files and tests belong to stream I).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// _ pins install.Fake's phase 0 signature (p0-decisions.md item 4).
var _ func(*game.Fake, string, []string) game.Installer = install.Fake

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestFakeInstall(t *testing.T) {
	root := t.TempDir()
	standIn := filepath.Join(root, "built", exeName("fakeserver"))
	os.MkdirAll(filepath.Dir(standIn), 0o755)
	os.WriteFile(standIn, []byte("the stand-in"), 0o755)

	server := filepath.Join(root, "games", "x", "server")
	program := filepath.Join(root, "games", "x", "runtime", "jdk", "bin", exeName("java"))
	files := []string{
		filepath.Join(server, "server.jar"),
		filepath.Join(server, "steamapps", "appmanifest_2394010.acf"),
		filepath.Join(root, "games", "x", "other", ".egs-build.json"),
		"lib/extra.so", // relative: beside the program
	}
	fake := &game.Fake{Program: standIn, Script: "join:Alex@1,leave:Alex@4", Speed: 60}
	var steps []game.Progress
	if err := install.Fake(fake, program, files).Install(context.Background(), false, func(p game.Progress) { steps = append(steps, p) }); err != nil {
		t.Fatal(err)
	}

	if b, err := os.ReadFile(program); err != nil || string(b) != "the stand-in" {
		t.Fatalf("program = %q, %v", b, err)
	}
	if st, _ := os.Stat(program); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Errorf("program mode %v isn't runnable", st.Mode())
	}
	var conf map[string]any
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(program), "fakeserver.json"))
	if err := json.Unmarshal(b, &conf); err != nil || conf["script"] != fake.Script || conf["speed"] != 60.0 || len(conf) != 2 {
		t.Errorf("fakeserver.json = %s (%v)", b, err)
	}
	// .egs-build.json wherever Build() might look: beside the program and
	// in the folder of every file listed.
	for _, dir := range []string{filepath.Dir(program), server, filepath.Join(server, "steamapps"), filepath.Join(root, "games", "x", "other"), filepath.Join(filepath.Dir(program), "lib")} {
		var build game.Build
		b, err := os.ReadFile(filepath.Join(dir, ".egs-build.json"))
		if err != nil || json.Unmarshal(b, &build) != nil || build.Version == "" || build.Date.IsZero() {
			t.Errorf("%s/.egs-build.json = %s, %v", dir, b, err)
		}
	}
	// The Steam manifest reads as build 1, the way SteamBuild reads it.
	m, _ := os.ReadFile(files[1])
	if !regexp.MustCompile(`"buildid"\s+"1"`).Match(m) || !regexp.MustCompile(`"LastUpdated"\s+"\d+"`).Match(m) {
		t.Errorf("appmanifest = %s", m)
	}
	for _, f := range []string{files[0], filepath.Join(filepath.Dir(program), "lib", "extra.so")} {
		if st, err := os.Stat(f); err != nil || st.Size() != 0 {
			t.Errorf("placeholder %s: %v", f, err)
		}
	}
	if len(steps) == 0 || steps[len(steps)-1].Percent != 100 {
		t.Errorf("progress = %+v, want it to end at 100%%", steps)
	}

	// A repair copies the program again and leaves files alone.
	os.WriteFile(files[0], []byte("the server wrote this"), 0o644)
	os.WriteFile(standIn, []byte("a newer stand-in"), 0o755)
	if err := install.Fake(fake, program, files).Install(context.Background(), true, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(program); string(b) != "a newer stand-in" {
		t.Errorf("after a repair the program is %q", b)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "the server wrote this" {
		t.Errorf("a repair emptied %s", files[0])
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(program), ".fakeserver-*")); len(left) > 0 {
		t.Errorf("left behind %q", left)
	}
}

// TestFakeInstallDelay: the pretend download takes Fake.Delay, reporting
// progress, and stops when cancelled without installing anything.
func TestFakeInstallDelay(t *testing.T) {
	root := t.TempDir()
	standIn := filepath.Join(root, "fakeserver")
	os.WriteFile(standIn, []byte("x"), 0o755)
	program := filepath.Join(root, "server", exeName("demoserver"))

	start := time.Now()
	var pcts []float64
	if err := install.Fake(&game.Fake{Program: standIn, Delay: 300 * time.Millisecond}, program, nil).
		Install(context.Background(), false, func(p game.Progress) { pcts = append(pcts, p.Percent) }); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Errorf("the install took %v, less than its delay", took)
	}
	if len(pcts) < 3 {
		t.Errorf("progress = %v, want several steps", pcts)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	other := filepath.Join(root, "other", exeName("demoserver"))
	err := install.Fake(&game.Fake{Program: standIn, Delay: time.Minute}, other, nil).Install(ctx, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a cancelled install gave %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("a cancelled install left the program")
	}
}

func TestFakeInstallRefuses(t *testing.T) {
	root := t.TempDir()
	standIn := filepath.Join(root, "fakeserver")
	os.WriteFile(standIn, []byte("x"), 0o755)
	cases := map[string]struct {
		fake    *game.Fake
		program string
	}{
		"no stand-in":   {nil, filepath.Join(root, exeName("a"))},
		"no path":       {&game.Fake{}, filepath.Join(root, exeName("a"))},
		"relative path": {&game.Fake{Program: standIn}, exeName("a")},
		"missing":       {&game.Fake{Program: filepath.Join(root, "nothing")}, filepath.Join(root, exeName("a"))},
	}
	if runtime.GOOS == "windows" {
		cases["not .exe"] = struct {
			fake    *game.Fake
			program string
		}{&game.Fake{Program: standIn}, filepath.Join(root, "a")}
	}
	for name, c := range cases {
		if err := install.Fake(c.fake, c.program, nil).Install(context.Background(), false, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
