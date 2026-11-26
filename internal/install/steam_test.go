package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/steamcmd/steamcmdtest"
)

func steamDirs(t *testing.T, script string) game.Dirs {
	t.Helper()
	root := t.TempDir()
	d := game.Dirs{Root: root, Server: filepath.Join(root, "server"), SteamCMD: filepath.Join(root, "steamcmd")}
	steamcmdtest.Put(t, d.SteamCMD, script)
	return d
}

func TestSteamCMD(t *testing.T) {
	d := steamDirs(t, ` Update state (0x61) downloading, progress: 25.00 (25 / 100)
@success
`)
	var lines []string
	inst := SteamCMD(SteamApp{AppID: "1169370", Name: "Necesse", SizeGB: 0.5}, d, func(l string) { lines = append(lines, l) })
	var steps []game.Progress
	if err := inst.Install(context.Background(), true, func(p game.Progress) { steps = append(steps, p) }); err != nil {
		t.Fatal(err)
	}
	want := []game.Progress{
		{Step: "Connecting to Steam…", Percent: -1},
		{Step: "Downloading the Necesse server…", Percent: 25},
		{Step: "Server files are up to date.", Percent: 100},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("progress = %v, want %v", steps, want)
	}
	if len(lines) != 2 || lines[1] != "Success! App '1169370' fully installed." {
		t.Errorf("output = %q", lines)
	}
	runs := steamcmdtest.Args(t, d.SteamCMD)
	if len(runs) != 1 || !reflect.DeepEqual(runs[0], []string{"+force_install_dir", d.Server, "+login", "anonymous", "+app_update", "1169370", "validate", "+quit"}) {
		t.Errorf("runs = %q", runs)
	}

	// A branch goes to SteamCMD, and a nil progress is fine.
	inst = SteamCMD(SteamApp{AppID: "380870", Branch: "unstable", Name: "Project Zomboid"}, d, nil)
	if err := inst.Install(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	if runs := steamcmdtest.Args(t, d.SteamCMD); !reflect.DeepEqual(runs[1][4:], []string{"+app_update", "380870", "-beta", "unstable", "+quit"}) {
		t.Errorf("runs = %q", runs)
	}

	// No app id would mean Valheim's to SteamCMD: refused, nothing runs.
	if err := SteamCMD(SteamApp{Name: "Oops"}, d, nil).Install(context.Background(), false, nil); err == nil {
		t.Error("an install with no app id worked")
	}
	if err := SteamCMD(SteamApp{AppID: "1169370"}, game.Dirs{}, nil).Install(context.Background(), false, nil); err == nil {
		t.Error("an install with no folders worked")
	}
	if n := len(steamcmdtest.Args(t, d.SteamCMD)); n != 2 {
		t.Errorf("SteamCMD ran %d times", n)
	}
}

func TestSteamBuild(t *testing.T) {
	dir := t.TempDir()
	if _, ok := SteamBuild(dir, "2394010"); ok {
		t.Error("SteamBuild found a build in an empty folder")
	}
	os.MkdirAll(filepath.Join(dir, "steamapps"), 0o755)
	manifest := "\"AppState\"\n{\n\t\"appid\"\t\t\"2394010\"\n\t\"buildid\"\t\t\"20698123\"\n\t\"LastUpdated\"\t\t\"1764150000\"\n}\n"
	os.WriteFile(filepath.Join(dir, "steamapps", "appmanifest_2394010.acf"), []byte(manifest), 0o644)
	b, ok := SteamBuild(dir, "2394010")
	if !ok || b.Build != "20698123" || !b.Date.Equal(time.Unix(1764150000, 0)) || b.Version != "" {
		t.Errorf("SteamBuild = %+v, %v", b, ok)
	}
}

// Spec 2.3's acceptance: a start's update (here Valheim's, through
// internal/steamcmd as its adapter uses it) never waits while another game's
// 10-minute install, through this package, holds the one SteamCMD: it is
// skipped in well under 30 s with the manager's warning text, and the
// install of a third game waits instead.
func TestStartUpdateSkipsWhileAnotherInstalls(t *testing.T) {
	d := steamDirs(t, "@success\n")
	steamcmdtest.SetScriptFor(t, d.SteamCMD, 1, ` Update state (0x61) downloading, progress: 3.00 (3 / 100)
@touch downloading
@sleep 10m
@success
`)
	satisfactory := SteamCMD(SteamApp{AppID: "1690800", Name: "Satisfactory", SizeGB: 15.5}, d, nil)
	ctx, cancel := context.WithCancel(context.Background())
	bigDone := make(chan error, 1)
	go func() { bigDone <- satisfactory.Install(ctx, false, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-bigDone:
		case <-time.After(30 * time.Second):
			t.Error("the big install didn't stop")
		}
	})
	steamcmdtest.Await(t, d.SteamCMD, "downloading", 20*time.Second)

	for _, start := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"Valheim", func(ctx context.Context) error {
			v := &steamcmd.SteamCMD{Dir: d.SteamCMD, InstallDir: filepath.Join(d.Root, "valheim")}
			return v.Install(ctx, false, func(steamcmd.Progress) {})
		}},
		{"Necesse", func(ctx context.Context) error {
			return SteamCMD(SteamApp{AppID: "1169370", Name: "Necesse"}, d, nil).Install(ctx, false, nil)
		}},
	} {
		began := time.Now()
		err := start.run(SkipIfBusy(context.Background()))
		if took := time.Since(began); took >= 30*time.Second {
			t.Errorf("%s's start waited %v", start.name, took)
		}
		var busy *BusyError
		if !errors.Is(err, ErrBusy) || !errors.As(err, &busy) || busy.Name != "Satisfactory" {
			t.Fatalf("%s's update = %v", start.name, err)
		}
		warning := "Couldn't check for server updates (" + err.Error() + "). Starting the version that's installed."
		if warning != "Couldn't check for server updates (Satisfactory is downloading). Starting the version that's installed." {
			t.Errorf("warning = %q", warning)
		}
	}
	if n := len(steamcmdtest.Args(t, d.SteamCMD)); n != 1 {
		t.Fatalf("SteamCMD ran %d times while the big install held it", n)
	}

	// An install (not a start) waits its turn, saying for what.
	var mu sync.Mutex
	var steps []string
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- SteamCMD(SteamApp{AppID: "2394010", Name: "Palworld"}, d, nil).Install(context.Background(), false, func(p game.Progress) {
			mu.Lock()
			steps = append(steps, p.Step)
			mu.Unlock()
		})
	}()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		shown := strings.Join(steps, "\n")
		mu.Unlock()
		if strings.Contains(shown, "Waiting for the Satisfactory download to finish…") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiting install never said what it waits for")
		}
	}
	cancel()
	if err := <-bigDone; err == nil || err.Error() != "cancelled" {
		t.Errorf("the big install = %v", err)
	}
	bigDone <- nil // for the cleanup
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the waiting install never ran")
	}
}
