package terraria

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

func TestNewest(t *testing.T) {
	recorded, err := os.ReadFile(filepath.Join("testdata", "dedicated-servers-names.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body, name, version string
	}{
		// What terraria.org answered on 2026-11-26: the same name twice.
		{string(recorded), "terraria-server-1461.zip", "1.4.6.1"},
		{`["terraria-server-1449.zip","terraria-server-1461.zip","terraria-server-145.zip"]`, "terraria-server-1461.zip", "1.4.6.1"},
		{`["terraria-server-145.zip","terraria-server-1449.zip"]`, "terraria-server-145.zip", "1.4.5"},
		{`["readme.txt","terraria-server-1462.zip","terraria-server-1461.zip"]`, "terraria-server-1462.zip", "1.4.6.2"},
	} {
		rel, err := newest([]byte(c.body))
		if err != nil || rel.Name != c.name || rel.Version != c.version {
			t.Errorf("%s: %+v, %v; want %s (%s)", c.body, rel, err, c.name, c.version)
		}
	}
	for _, bad := range []string{`[]`, `["something.zip"]`, `{"names":[]}`, `<html>`} {
		if _, err := newest([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if !newer("1.4.6.1", "1.4.6") || !newer("1.4.6", "1.4.5.7") || newer("1.4.6.1", "1.4.6.1") || newer("1.4.4.9", "1.4.5") {
		t.Error("newer")
	}
}

// zipEntry is one line of testdata/terraria-server-1461.entries.
type zipEntry struct {
	name string
	size int
}

func realEntries(t *testing.T) []zipEntry {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "terraria-server-1461.entries"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []zipEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r") // a Windows checkout may end lines with CRLF
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			t.Fatalf("bad entries line %q", line)
		}
		size, _ := strconv.Atoi(parts[2])
		out = append(out, zipEntry{name: parts[3], size: size})
	}
	return out
}

// serverZip is a small zip laid out as the real one: the same names under
// a version folder (renamed to top), made on Windows (FAT, so no Unix
// permissions), with a few bytes standing in for each file.
func serverZip(t *testing.T, top string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range realEntries(t) {
		name := top + strings.TrimPrefix(e.name, "1461")
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, CreatorVersion: 20} // creator 0: MS-DOS/FAT
		if strings.HasSuffix(name, "/") {
			h.ExternalAttrs = 0x10 // directory
			if _, err := zw.CreateHeader(h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		h.ExternalAttrs = 0x20 // archive
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("stand-in for " + name + " (" + strconv.Itoa(e.size) + " bytes)\n"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeSite answers terraria.org's two calls from recorded answers, and
// counts the downloads.
type fakeSite struct {
	mu        sync.Mutex
	list      string
	zips      map[string][]byte
	downloads int
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == listPath:
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(f.list))
	case strings.HasPrefix(r.URL.Path, downloadPath):
		z, ok := f.zips[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.downloads++
		w.Header().Set("Content-Type", "application/zip")
		w.Write(z)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSite) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}

// useSite points the adapter at f for the test, and stands in for XNA's
// installer on Windows.
func useSite(t *testing.T, f *fakeSite) *int {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	savedSite, savedXNA := site, ensureXNA
	site = srv.URL
	xnaCalls := 0
	ensureXNA = func(context.Context, func(game.Progress)) error { xnaCalls++; return nil }
	t.Cleanup(func() { site, ensureXNA = savedSite, savedXNA })
	return &xnaCalls
}

// TestInstall installs from a recorded terraria.org: only this system's
// folder is kept, as server/<Windows|Mac|Linux>, its program is runnable,
// the version is noted, and nothing is left of the download.
func TestInstall(t *testing.T) {
	recorded, err := os.ReadFile(filepath.Join("testdata", "dedicated-servers-names.json"))
	if err != nil {
		t.Fatal(err)
	}
	site := &fakeSite{list: string(recorded), zips: map[string][]byte{"terraria-server-1461.zip": serverZip(t, "1461")}}
	xnaCalls := useSite(t, site)
	s, _ := newTestServer(t)
	ctx := context.Background()
	var steps []string
	progress := func(p game.Progress) { steps = append(steps, p.Step) }

	if err := s.Installer().Install(ctx, false, progress); err != nil {
		t.Fatal(err)
	}
	program := s.Program()
	st, err := os.Stat(program)
	if err != nil {
		t.Fatalf("no program after the install: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 {
		t.Errorf("%s isn't runnable (%v): the zip stores no Unix permissions, so the install must set them", program, st.Mode())
	}
	if b, ok := s.Build(); !ok || b.Version != "1.4.6.1" {
		t.Errorf("Build() = %+v, %v", b, ok)
	}
	entries, _ := os.ReadDir(s.env.Dirs.Server)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{".egs-build.json", osFolder(runtime.GOOS)}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("the server folder holds %q, want %q", names, want)
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(program), "osx", "libGNS_Wrapper.dylib")); err != nil {
			t.Errorf("the Mac libraries aren't beside the program: %v", err)
		}
	}
	if got := site.count(); got != 1 {
		t.Errorf("%d downloads", got)
	}
	if runtime.GOOS == "windows" && *xnaCalls != 1 {
		t.Errorf("XNA was checked %d times, want once", *xnaCalls)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "Downloading the Terraria server 1.4.6.1") {
		t.Errorf("progress steps: %q", steps)
	}

	// Installed and current: nothing to download. A repair downloads again.
	if err := s.Installer().Install(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := site.count(); got != 1 {
		t.Errorf("an install of the version that is there downloaded it again (%d downloads)", got)
	}
	os.Remove(program)
	if _, ok := s.Build(); ok {
		t.Error("Build() says installed with the program gone")
	}
	if err := s.Installer().Install(ctx, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(program); err != nil || site.count() != 2 {
		t.Errorf("the repair: %v, %d downloads", err, site.count())
	}
}

// TestUpdateOnStart: a start with "update on start" installs a newer
// server first and keeps what lives beside it (the config, the ban list);
// when terraria.org can't be reached it starts the installed one.
func TestUpdateOnStart(t *testing.T) {
	withXNA(t)
	site := &fakeSite{list: `["terraria-server-1461.zip"]`, zips: map[string][]byte{"terraria-server-1461.zip": serverZip(t, "1461")}}
	useSite(t, site)
	s, _ := newTestServer(t)
	ctx := context.Background()
	if err := s.Installer().Install(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	banlist := filepath.Join(s.env.Dirs.Server, "banlist.txt")
	os.WriteFile(banlist, []byte("Griefer\n"), 0o644)
	stale := filepath.Join(s.env.Dirs.Server, osFolder(runtime.GOOS), "only-in-1461.txt")
	os.WriteFile(stale, nil, 0o644)

	site.mu.Lock()
	site.list = `["terraria-server-1461.zip","terraria-server-1462.zip"]`
	site.zips["terraria-server-1462.zip"] = serverZip(t, "1462")
	site.mu.Unlock()
	req := game.PlanRequest{World: "Forest", New: true, Saved: testSaved(), Update: true, LogPath: filepath.Join(s.env.Dirs.Logs, "x.log")}
	if _, err := s.Plan(ctx, req); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Build(); b.Version != "1.4.6.2" {
		t.Errorf("after the update the version is %q", b.Version)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the old version's files are still there")
	}
	if b, err := os.ReadFile(banlist); err != nil || string(b) != "Griefer\n" {
		t.Errorf("the ban list: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(s.env.Dirs.Server, workDir)); err == nil {
		t.Error("the install's working folder was left behind")
	}

	// terraria.org is down: the start goes on with what's installed.
	site.mu.Lock()
	site.list = `<html>down</html>`
	site.mu.Unlock()
	before := site.count()
	if _, err := s.Plan(ctx, req); err != nil {
		t.Errorf("a start with terraria.org down: %v", err)
	}
	if site.count() != before {
		t.Error("downloaded something with the list unreadable")
	}
}

// TestInstallBadZip: a zip without this system's server leaves the
// installed one as it was.
func TestInstallBadZip(t *testing.T) {
	site := &fakeSite{list: `["terraria-server-1461.zip"]`, zips: map[string][]byte{"terraria-server-1461.zip": serverZip(t, "1461")}}
	useSite(t, site)
	s, _ := newTestServer(t)
	ctx := context.Background()
	if err := s.Installer().Install(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("1462/readme.txt")
	w.Write([]byte("nothing here"))
	zw.Close()
	site.mu.Lock()
	site.list = `["terraria-server-1462.zip"]`
	site.zips["terraria-server-1462.zip"] = buf.Bytes()
	site.mu.Unlock()
	if err := s.Installer().Install(ctx, false, nil); err == nil {
		t.Error("a zip without the server installed")
	}
	if b, ok := s.Build(); !ok || b.Version != "1.4.6.1" {
		t.Errorf("the installed server changed: %+v, %v", b, ok)
	}
}

// The XNA installer is pinned to Microsoft's own file.
func TestXNAPrereq(t *testing.T) {
	p := xnaPrereq
	if p.Hash != (install.Hash{Algo: "sha256", Hex: "47260420773a20443fb1e38a89b7f39c5237a80a842de598e6c8f7f90a3bbd6d"}) ||
		!strings.HasPrefix(p.URL, "https://download.microsoft.com/") || !strings.HasSuffix(p.URL, "/xnafx40_redist.msi") ||
		strings.Join(p.Args, " ") != "/quiet /norestart" || p.Installed == nil {
		t.Errorf("prereq %+v", p)
	}
}
