package mods

import (
	"archive/zip"
	"bytes"
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
	var done []float64 // how far each download got
	res, err := in.Prepare(ctx, []string{"advancedportals"}, false, func(s string, pct float64) {
		if pct < 0 {
			steps = append(steps, s)
			done = append(done, 0)
		} else {
			done[len(done)-1] = pct
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Installed BepInEx 5.4.2351.", "Installed Jötunn 2.30.2.", "Installed Advanced Portals 1.2.0."}
	if strings.Join(res.Changed, "|") != strings.Join(want, "|") {
		t.Fatalf("changed %q, want %q", res.Changed, want)
	}
	if len(steps) != 4 || done[1] != 100 || done[2] != 100 || done[3] != 100 {
		t.Fatalf("steps %q, downloaded %v%%", steps, done)
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

func TestPrepareNewMods(t *testing.T) {
	in, _, _ := setup(t)
	res, err := in.Prepare(context.Background(), []string{"riverheim", "planteverything", "heightmapunlimited", "gammaofnightlights"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	plugin := func(rel ...string) []string { return append([]string{"BepInEx", "plugins"}, rel...) }
	for _, f := range [][]string{
		plugin("Gurebu-Riverheim", "Riverheim.dll"), // BepInEx/plugins/ in its zip
		plugin("Advize-PlantEverything", "Advize_PlantEverything.dll"),
		plugin("Digitalroot-Heightmap_Unlimited_JvL", "Digitalroot.HeightmapUnlimitedJvL.dll"),
		plugin("ValheimModding-Jotunn", "Jotunn.dll"),
		plugin("shudnal-GammaOfNightLights", "GammaOfNightLights.dll"),
		plugin("shudnal-ConditionalConfigSync", "ConditionalConfigSync.Plugin.dll"),
	} {
		if !exists(in.Dir, f...) {
			t.Errorf("missing %s", filepath.Join(f...))
		}
	}
	if exists(in.Dir, plugin("Gurebu-Riverheim", "BepInEx")...) || exists(in.Dir, plugin("Gurebu-Riverheim", "CHANGELOG.md")...) {
		t.Error("Riverheim's zip was unpacked as it is")
	}
	if !strings.Contains(strings.Join(res.Changed, " "), "Installed Conditional Config Sync 1.0.11.") {
		t.Errorf("changed %q", res.Changed)
	}
}

func TestPrepareRemovesModsNoLongerOffered(t *testing.T) {
	in, _, _ := setup(t)
	ctx := context.Background()
	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	// A mod an earlier version offered, and a library only it used.
	for _, name := range []string{"Marf-FuelEternal", "Some-Library"} {
		dir := filepath.Join(in.Dir, "BepInEx", "plugins-off", name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "x.dll"), []byte("old"), 0o644)
	}
	rec := in.read()
	rec.Packages["Marf-FuelEternal"] = installed{Version: "1.2.1", Deps: []string{Loader, "Some-Library"}}
	rec.Packages["Some-Library"] = installed{Version: "1.0.0"}
	if err := in.write(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Prepare(ctx, []string{"advancedportals"}, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Marf-FuelEternal", "Some-Library"} {
		if exists(in.Dir, "BepInEx", "plugins-off", name) || in.Installed()[name] != "" {
			t.Errorf("%s is still there", name)
		}
	}
	if !exists(in.Dir, "BepInEx", "plugins", "RandyKnapp-AdvancedPortals") || !exists(in.Dir, "BepInEx", "plugins", "ValheimModding-Jotunn") {
		t.Error("an offered mod or its library was removed")
	}
}

// More World Locations keeps its asset bundles in plugins/Bundles and looks
// for them in its own folder, as the mod managers lay it out.
func TestPrepareMoreWorldLocations(t *testing.T) {
	in, _, _ := setup(t)
	res, err := in.Prepare(context.Background(), []string{"moreworldlocations"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "Installed BepInEx 5.4.2351.|Installed Jötunn 2.30.2.|Installed YamlDotNet 16.3.1.|Installed Json.NET 13.0.4.|Installed More World Locations 5.2.2."
	if got := strings.Join(res.Changed, "|"); got != want {
		t.Fatalf("changed %q", got)
	}
	dir := filepath.Join("BepInEx", "plugins", "warpalicious-More_World_Locations_AIO")
	for _, rel := range [][]string{
		{dir, "More_World_Locations_AIO.dll"}, {dir, "assetBundleManifest_full"},
		{dir, "Bundles", "mwl_meadowsbarn1"}, {dir, "Bundles", "cd_entrance2"},
		{"BepInEx", "plugins", "ValheimModding-JsonDotNET", "Newtonsoft.Json.dll"},
		{"BepInEx", "plugins", "ValheimModding-YamlDotNet", "YamlDotNet.dll"},
		{"BepInEx", "plugins", "ValheimModding-Jotunn", "Jotunn.dll"},
	} {
		if !exists(in.Dir, rel...) {
			t.Errorf("%s is missing", filepath.Join(rel...))
		}
	}
	if exists(in.Dir, dir, "plugins") || exists(in.Dir, dir, "CHANGELOG.md") {
		t.Error("the package wasn't laid out like a mod manager does")
	}
}

// Mods just for players' own games stay off the server.
func TestPlayersOnly(t *testing.T) {
	if got := strings.Join(ServerMods([]string{"farlands", "seasonality", "gone"}), ","); got != "seasonality" {
		t.Fatalf("ServerMods = %q", got)
	}
	in, fake, _ := setup(t)
	ctx := context.Background()
	if _, err := in.Prepare(ctx, []string{"seasonality"}, false, nil); err != nil {
		t.Fatal(err)
	}
	before := fake.Requests.Load()
	res, err := in.Prepare(ctx, []string{"farlands"}, true, nil)
	if err != nil || len(res.Changed) != 0 || fake.Requests.Load() != before {
		t.Fatalf("a players' mod was fetched: %v %+v", err, res)
	}
	if exists(in.Dir, "winhttp.dll") || !exists(in.Dir, "winhttp.dll.off") {
		t.Error("the mod loader is still on for a world with only a players' mod")
	}
}

func TestCatalog(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range Catalog {
		if ids[m.ID] || !safeName.MatchString(m.Package) || m.Name == "" || m.Short == "" || m.Summary == "" ||
			!strings.HasPrefix(m.URL, "https://thunderstore.io/c/valheim/p/") {
			t.Errorf("bad catalog entry %+v", m)
		}
		ids[m.ID] = true
		if m.Group != GroupWorld && m.Group != GroupPlay && m.Group != GroupLook || m.Players != Required && m.Players != Recommended && m.Players != PlayersOnly {
			t.Errorf("%s: group %q, players %q", m.ID, m.Group, m.Players)
		}
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

// The record names folders that get moved and deleted, so a changed record
// can't point anywhere else.
func TestRecordNamesStayInside(t *testing.T) {
	dir := t.TempDir()
	in := &Installer{Dir: filepath.Join(dir, "server")}
	victim := filepath.Join(dir, "victim")
	os.MkdirAll(victim, 0o755)
	os.MkdirAll(filepath.Join(in.Dir, "BepInEx", "plugins"), 0o755)
	in.write(record{Packages: map[string]installed{
		"x/../../../victim":          {Version: "1.0.0"},
		"RandyKnapp-AdvancedPortals": {Version: "1.2.0"},
	}})
	rec := in.read()
	if _, ok := rec.Packages["x/../../../victim"]; ok || len(rec.Packages) != 1 {
		t.Fatalf("record = %+v", rec.Packages)
	}
	if err := in.switchPlugin("x/../../../victim", false); err == nil {
		t.Fatal("moved a folder by an unsafe name")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a folder outside the server was touched")
	}
}

// The BepInEx pack can't replace the server or this app's record.
func TestLoaderCantReplaceTheServer(t *testing.T) {
	for _, name := range []string{"valheim_server.exe", "BepInEx/valheim-easy-server.json"} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(loaderRoot + name)
		w.Write([]byte("evil"))
		zw.Close()
		zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		in := &Installer{Dir: t.TempDir()}
		if err := in.unpackLoader(zr); err == nil {
			t.Errorf("%s: unpacked", name)
		}
		if exists(in.Dir, name) {
			t.Errorf("%s was written", name)
		}
	}
}
