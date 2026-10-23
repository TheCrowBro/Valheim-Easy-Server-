package mods

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"valheim-easy-server/internal/mods/modstest"
)

func setup(t *testing.T) (*Installer, *modstest.Fake, *httptest.Server) {
	t.Helper()
	fake := modstest.New()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &Installer{Dir: t.TempDir(), URL: srv.URL}, fake, srv
}

func read(t *testing.T, dir string, rel ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir string, rel ...string) bool {
	_, err := os.Stat(filepath.Join(append([]string{dir}, rel...)...))
	return err == nil
}

func TestPrepareInstallsAndSwitches(t *testing.T) {
	in, fake, _ := setup(t)
	ctx := context.Background()
	var steps []string
	res, err := in.Prepare(ctx, []string{"advancedportals"}, false, func(s string) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Installed BepInEx 5.4.2351.", "Installed Jötunn 2.30.2.", "Installed Advanced Portals 1.2.0."}
	if strings.Join(res.Changed, "|") != strings.Join(want, "|") {
		t.Fatalf("changed %q, want %q", res.Changed, want)
	}
	if len(steps) != 4 {
		t.Fatalf("steps %q", steps)
	}
	for _, rel := range [][]string{
		{"winhttp.dll"}, {"doorstop_config.ini"}, {"BepInEx", "core", "BepInEx.Preloader.dll"},
		{"BepInEx", "plugins", "ValheimModding-Jotunn", "Jotunn.dll"},
		{"BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "AdvancedPortals.dll"},
	} {
		if !exists(in.Dir, rel...) {
			t.Errorf("%s is missing", filepath.Join(rel...))
		}
	}
	for _, rel := range [][]string{{"manifest.json"}, {"BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "icon.png"}, {"BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "README.md"}} {
		if exists(in.Dir, rel...) {
			t.Errorf("%s shouldn't be unpacked", filepath.Join(rel...))
		}
	}
	cfg := read(t, in.Dir, "BepInEx", "config", "BepInEx.cfg")
	if !strings.Contains(cfg, "[Logging.Console]\r\n\r\n## Enables showing a console for log output.\r\nEnabled = false\r\n") ||
		!strings.Contains(cfg, "[Logging.Disk]\r\n\r\nEnabled = true") {
		t.Fatalf("console not turned off, or too much changed:\n%q", cfg)
	}
	if got := in.Installed(); got[Loader] != "5.4.2351" || got["RandyKnapp-AdvancedPortals"] != "1.2.0" {
		t.Fatalf("installed %v", got)
	}

	// Starting again needs no download.
	n := fake.Requests.Load()
	if res, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil || len(res.Changed) != 0 {
		t.Fatalf("second prepare: %v %v", res, err)
	}
	if fake.Requests.Load() != n {
		t.Fatal("the second prepare went online")
	}

	// An update replaces only what changed, and old files go.
	fake.SetVersion(modstest.Portal, "1.3.0")
	os.WriteFile(filepath.Join(in.Dir, "BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "old.dll"), []byte("x"), 0o644)
	res, err = in.Prepare(ctx, []string{"advancedportals"}, true, nil)
	if err != nil || strings.Join(res.Changed, "|") != "Installed Advanced Portals 1.3.0." {
		t.Fatalf("update: %v %v", res, err)
	}
	if exists(in.Dir, "BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "old.dll") ||
		read(t, in.Dir, "BepInEx", "plugins", "RandyKnapp-AdvancedPortals", "AdvancedPortals.dll") != "portals 1.3.0" {
		t.Fatal("the old copy wasn't replaced")
	}

	// Off moves the loader aside; on puts it back.
	if _, err := in.Prepare(ctx, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if exists(in.Dir, "winhttp.dll") || !exists(in.Dir, "winhttp.dll.off") {
		t.Fatal("the loader is still on")
	}
	n = fake.Requests.Load()
	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if !exists(in.Dir, "winhttp.dll") || exists(in.Dir, "winhttp.dll.off") || fake.Requests.Load() != n {
		t.Fatal("turning the loader back on failed or went online")
	}
}

func TestPrepareKeepsOtherWorldsModsAside(t *testing.T) {
	in, fake, _ := setup(t)
	ctx := context.Background()
	plugin := func(name string) string {
		switch {
		case exists(in.Dir, "BepInEx", "plugins", name):
			return "on"
		case exists(in.Dir, "BepInEx", "plugins-off", name):
			return "aside"
		}
		return "missing"
	}
	state := func() string {
		return strings.Join([]string{plugin(modstest.Portal), plugin(modstest.Jotunn), plugin(modstest.Seasons), plugin(modstest.Yaml)}, " ")
	}

	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	// A world with only Seasonality: the portals and Jötunn wait aside.
	res, err := in.Prepare(ctx, []string{"seasonality"}, false, nil)
	if err != nil || strings.Join(res.Changed, "|") != "Installed YamlDotNet 16.3.1.|Installed Seasonality 3.8.4." {
		t.Fatalf("seasonality: %v %v", res, err)
	}
	if got := state(); got != "aside aside on on" {
		t.Fatalf("seasonality only: %s", got)
	}
	if !exists(in.Dir, "BepInEx", "plugins", modstest.Seasons, "Seasonality.dll") ||
		read(t, in.Dir, "BepInEx", "config", "Seasonality", "Textures", "winter.png") != "snow" ||
		exists(in.Dir, "BepInEx", "plugins", modstest.Yaml, "LICENSE.txt") {
		t.Fatal("Seasonality isn't laid out like a mod manager would")
	}
	// Both, without going online again; then back to the portals only.
	n := fake.Requests.Load()
	if _, err := in.Prepare(ctx, []string{"advancedportals", "seasonality"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "on on on on" || fake.Requests.Load() != n {
		t.Fatalf("both: %s (went online: %v)", got, fake.Requests.Load() != n)
	}
	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "on on aside aside" {
		t.Fatalf("portals only: %s", got)
	}
	// An update of a package that's aside replaces it there too.
	fake.SetVersion(modstest.Seasons, "3.9.0")
	if _, err := in.Prepare(ctx, []string{"seasonality"}, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "aside aside on on" || read(t, in.Dir, "BepInEx", "plugins", modstest.Seasons, "Seasonality.dll") != "seasons 3.9.0" {
		t.Fatalf("after update: %s", got)
	}
}

func TestPrepareOffline(t *testing.T) {
	in, _, srv := setup(t)
	ctx := context.Background()
	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	res, err := in.Prepare(ctx, []string{"advancedportals"}, true, nil)
	if err != nil || res.Warning == "" {
		t.Fatalf("offline with mods installed: %v %v", res, err)
	}

	empty := &Installer{Dir: t.TempDir(), URL: srv.URL}
	if _, err := empty.Prepare(ctx, []string{"advancedportals"}, false, nil); err == nil {
		t.Fatal("offline without mods installed should fail")
	}
	if _, err := empty.Prepare(ctx, []string{"nope"}, false, nil); err == nil {
		t.Fatal("an unknown mod was accepted")
	}
}

func TestPrepareRefusesUnsafeFiles(t *testing.T) {
	in, fake, _ := setup(t)
	fake.AddFile(modstest.Portal, "plugins/../../../valheim_server.exe", "evil")
	if _, err := in.Prepare(context.Background(), []string{"advancedportals"}, false, nil); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("err = %v", err)
	}
	if exists(in.Dir, "valheim_server.exe") {
		t.Fatal("a file was written outside the plugin folder")
	}
}

func TestPrepareLeavesHandInstalledLoader(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "winhttp.dll"), []byte("someone else's"), 0o644)
	in := &Installer{Dir: dir, URL: "http://127.0.0.1:1"}
	if _, err := in.Prepare(context.Background(), nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if !exists(dir, "winhttp.dll") {
		t.Fatal("a loader this app didn't install was switched off")
	}
}

func TestEnv(t *testing.T) {
	if Env("/srv", false) != nil {
		t.Fatal("env without mods")
	}
	env := strings.Join(Env("/srv", true), "\n")
	if runtime.GOOS == "linux" {
		if !strings.Contains(env, "LD_PRELOAD=libdoorstop_x64.so") || !strings.Contains(env, "DOORSTOP_TARGET_ASSEMBLY=/srv/BepInEx/core/BepInEx.Preloader.dll") {
			t.Fatalf("env %q", env)
		}
	} else if env != "" {
		t.Fatalf("env %q", env)
	}
}
