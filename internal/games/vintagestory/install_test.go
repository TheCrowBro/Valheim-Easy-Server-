package vintagestory

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/install"
)

// TestParseRecordedListing: the version list as api.vintagestory.at gave it
// on 2026-11-26 (testdata/api/stable.json, 53 versions, newest first).
func TestParseRecordedListing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "api", "stable.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseListing(b, "windows")
	if err != nil {
		t.Fatal(err)
	}
	want := release{Version: "1.22.7", File: "vs_server_win-x64_1.22.7.zip",
		URL:  "https://cdn.vintagestory.at/gamefiles/stable/vs_server_win-x64_1.22.7.zip",
		Hash: install.Hash{Algo: "md5", Hex: "c5bcd15a558041533ed3b88f707bb19e"}}
	if r != want {
		t.Errorf("windows: %+v, want %+v", r, want)
	}
	if r, err := parseListing(b, "linux"); err != nil || r.Version != "1.22.7" || r.Hash.Hex != "3031212903b1c283084d5fe0cda6bce6" || !strings.HasSuffix(r.URL, "vs_server_linux-x64_1.22.7.tar.gz") {
		t.Errorf("linux: %+v, %v", r, err)
	}
	if _, err := parseListing(b, "darwin"); !errors.Is(err, errNoServerHere) {
		t.Errorf("darwin: %v, want no server", err)
	}
	if latest, _ := os.ReadFile(filepath.Join("testdata", "api", "lateststable.txt")); strings.TrimSpace(string(latest)) != r.Version {
		t.Errorf("lateststable.txt says %q", latest)
	}

	// Without the "latest" mark, the highest version with a server wins,
	// whatever the order; versions are compared number by number.
	unmarked := `{"1.9.14":{"windowsserver":{"filename":"a.zip","md5":"` + strings.Repeat("a", 32) + `","urls":{"cdn":"https://cdn.vintagestory.at/a.zip"}}},
		"1.22.7":{"windowsserver":{"filename":"b.zip","md5":"` + strings.Repeat("b", 32) + `","urls":{"cdn":"https://cdn.vintagestory.at/b.zip"}}},
		"1.22.10":{"windows":{"filename":"client.exe"}},
		"1.10.19":{"windowsserver":{"filename":"c.zip","md5":"` + strings.Repeat("c", 32) + `","urls":{"cdn":"https://cdn.vintagestory.at/c.zip"}}}}`
	if r, err := parseListing([]byte(unmarked), "windows"); err != nil || r.Version != "1.22.7" {
		t.Errorf("unmarked: %+v, %v", r, err)
	}
	for name, bad := range map[string]string{
		"no checksum": `{"1.22.7":{"windowsserver":{"filename":"b.zip","urls":{"cdn":"https://cdn.vintagestory.at/b.zip"}}}}`,
		"plain http":  `{"1.22.7":{"windowsserver":{"filename":"b.zip","md5":"` + strings.Repeat("b", 32) + `","urls":{"cdn":"http://cdn.vintagestory.at/b.zip"}}}}`,
		"no server":   `{"1.22.7":{"windows":{"filename":"client.exe"}}}`,
		"odd version": `{"../1":{"windowsserver":{"filename":"b.zip","md5":"` + strings.Repeat("b", 32) + `","urls":{"cdn":"https://cdn.vintagestory.at/b.zip"}}}}`,
		"damaged":     `{"1.22.7":{"windowsserver":`,
		"not a list":  `[1,2]`,
	} {
		if _, err := parseListing([]byte(bad), "windows"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.22.10", "1.22.9", true}, {"1.22.7", "1.22.7", false}, {"1.9.14", "1.10.0", false}, {"2.0", "1.99.99", true}, {"1.22.7.1", "1.22.7", true}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// site is a stand-in for api.vintagestory.at and its CDN, serving server
// archives that look like the real ones (VintagestoryServer.dll and its
// runtimeconfig.json at the top, no folder above them).
type site struct {
	srv       *httptest.Server
	versions  map[string][]byte // version -> archive
	channel   map[string]string // version -> .NET channel its runtimeconfig names
	latest    string
	badMD5    bool
	noListing bool
	archives  atomic.Int32
}

func newSite(t *testing.T) *site {
	s := &site{versions: map[string][]byte{}, channel: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	apiBase0, cdnBase0 := apiBase, cdnBase
	apiBase, cdnBase = s.srv.URL+"/api", s.srv.URL+"/cdn"
	t.Cleanup(func() { apiBase, cdnBase = apiBase0, cdnBase0 })
	return s
}

func (s *site) add(t *testing.T, version, channel string) {
	s.channel[version] = channel
	s.versions[version] = serverArchive(t, version, channel)
	s.latest = version
}

func (s *site) serve(w http.ResponseWriter, r *http.Request) {
	key, pattern, _ := serverPackage(runtime.GOOS)
	switch {
	case r.URL.Path == "/api/stable.json":
		if s.noListing {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		list := map[string]map[string]any{}
		for v, b := range s.versions {
			sum := md5.Sum(b)
			hexSum := hex.EncodeToString(sum[:])
			if s.badMD5 {
				hexSum = strings.Repeat("0", 32)
			}
			file := strings.ReplaceAll(pattern, "%s", v)
			entry := map[string]any{"filename": file, "md5": hexSum, "urls": map[string]string{"cdn": s.srv.URL + "/cdn/gamefiles/stable/" + file}}
			if v == s.latest {
				entry["latest"] = 1
			}
			list[v] = map[string]any{key: entry}
		}
		json.NewEncoder(w).Encode(list)
	case r.URL.Path == "/api/lateststable.txt":
		w.Write([]byte(s.latest + "\n"))
	case strings.HasPrefix(r.URL.Path, "/cdn/gamefiles/stable/"):
		for v, b := range s.versions {
			if strings.HasSuffix(r.URL.Path, "_"+v+".zip") || strings.HasSuffix(r.URL.Path, "_"+v+".tar.gz") {
				s.archives.Add(1)
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serverArchive is a small server download for this system: a zip on
// Windows, a .tar.gz elsewhere, as Anego ships them.
func serverArchive(t *testing.T, version, channel string) []byte {
	t.Helper()
	files := map[string]string{
		serverDLL:                               "server " + version,
		"VintagestoryServer.runtimeconfig.json": `{"runtimeOptions":{"tfm":"net` + channel + `","framework":{"name":"Microsoft.NETCore.App","version":"` + channel + `.0"}}}`,
		"assets/version-" + version + ".txt":    "",
	}
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for name, body := range files {
			w, _ := zw.Create(name)
			w.Write([]byte(body))
		}
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeDotNet stands in for install.DotNet: it lays out a runtime of the
// newest patch of channel the way .NET's own archive does, and notes it the
// way install does, so install.FindDotNet finds it.
type fakeDotNet struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeDotNet) install(ctx context.Context, channel, dir string, step func(game.Progress)) (string, error) {
	f.calls.Add(1)
	if f.fail {
		return "", errors.New("Microsoft's .NET site couldn't be reached")
	}
	version := channel + ".15"
	exe := filepath.Join(dir, "dotnet-"+version, map[bool]string{true: "dotnet.exe", false: "dotnet"}[runtime.GOOS == "windows"])
	os.MkdirAll(filepath.Join(filepath.Dir(exe), "shared", "Microsoft.NETCore.App", version), 0o755)
	if err := os.WriteFile(exe, []byte("dotnet"), 0o755); err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(dir, exe)
	note, _ := json.Marshal(map[string]string{"version": version, "path": filepath.ToSlash(rel)})
	return exe, os.WriteFile(filepath.Join(dir, ".egs-dotnet.json"), note, 0o644)
}

func useFakeDotNet(t *testing.T) *fakeDotNet {
	f := &fakeDotNet{}
	old := installDotNet
	installDotNet = f.install
	t.Cleanup(func() { installDotNet = old })
	return f
}

func TestInstall(t *testing.T) {
	if _, _, ok := serverPackage(runtime.GOOS); !ok {
		srv := Game{}.New(testEnv(t)).(*server)
		newSite(t)
		if err := srv.Installer().Install(t.Context(), false, nil); err == nil || !strings.Contains(err.Error(), "needs Windows") {
			t.Errorf("on %s: %v, want Needs Windows", runtime.GOOS, err)
		}
		return
	}
	site := newSite(t)
	site.add(t, "1.22.7", "10.0")
	dotnet := useFakeDotNet(t)
	env := testEnv(t)
	srv := Game{}.New(env).(*server)
	var steps []string
	progress := func(p game.Progress) { steps = append(steps, p.Step) }

	if err := srv.Installer().Install(t.Context(), false, progress); err != nil {
		t.Fatal(err)
	}
	b, ok := srv.Build()
	if !ok || b.Version != "1.22.7" {
		t.Fatalf("Build() = %+v, %v", b, ok)
	}
	if got := readFile(t, filepath.Join(env.Dirs.Server, "1.22.7", serverDLL)); got != "server 1.22.7" {
		t.Errorf("the dll holds %q", got)
	}
	if !strings.HasPrefix(srv.Program(), filepath.Join(env.Dirs.Runtime, "dotnet-10.0.15")) {
		t.Errorf("Program() = %s", srv.Program())
	}
	if !strings.Contains(strings.Join(steps, "|"), "Downloading the Vintage Story server 1.22.7") {
		t.Errorf("progress said %q", steps)
	}
	if entries, _ := os.ReadDir(env.Dirs.Server); len(entries) != 2 { // 1.22.7 and .egs-build.json
		t.Errorf("the server folder holds %d entries, want the version and the build file", len(entries))
	}

	// Installed and current: nothing is downloaded again.
	if err := srv.Installer().Install(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	if site.archives.Load() != 1 || dotnet.calls.Load() != 1 {
		t.Errorf("a second install downloaded %d archives and %d .NETs", site.archives.Load(), dotnet.calls.Load())
	}

	// A new version: its own folder, the old one removed, the same .NET.
	site.add(t, "1.22.8", "10.0")
	var said []string
	srv.updateOnStart(t.Context(), func(msg string, _ float64) { said = append(said, msg) })
	if b, _ := srv.Build(); b.Version != "1.22.8" {
		t.Errorf("after the update Build() = %+v (%q)", b, said)
	}
	if _, err := os.Stat(filepath.Join(env.Dirs.Server, "1.22.7")); !os.IsNotExist(err) {
		t.Errorf("the old version's folder is still there (%v)", err)
	}
	if dotnet.calls.Load() != 1 {
		t.Errorf(".NET was installed again for the same channel")
	}

	// A version that needs a .NET that can't be had: the update fails,
	// and the version that was installed is still the one that starts.
	site.add(t, "1.23.0", "11.0")
	dotnet.fail = true
	said = nil
	srv.updateOnStart(t.Context(), func(msg string, _ float64) { said = append(said, msg) })
	if b, _ := srv.Build(); b.Version != "1.22.8" {
		t.Errorf("after a failed update Build() = %+v", b)
	}
	if last := said[len(said)-1]; !strings.Contains(last, "Starting the version that's installed") {
		t.Errorf("the failed update said %q", said)
	}
	dotnet.fail = false

	// A wrong checksum: refused, and still the old version.
	site.badMD5 = true
	if err := srv.Installer().Install(t.Context(), false, nil); err == nil {
		t.Error("an archive with the wrong MD5 was installed")
	}
	if b, _ := srv.Build(); b.Version != "1.22.8" {
		t.Errorf("after a bad download Build() = %+v", b)
	}
	site.badMD5 = false

	// stable.json down: lateststable.txt names it, the CDN has it.
	site.noListing = true
	if err := srv.Installer().Install(t.Context(), false, nil); err != nil {
		t.Fatalf("with only lateststable.txt: %v", err)
	}
	if b, _ := srv.Build(); b.Version != "1.23.0" {
		t.Errorf("with only lateststable.txt Build() = %+v", b)
	}
	if !hasRuntime(srv.Program(), "11.0") {
		t.Error("the 1.23.0 server didn't get .NET 11.0")
	}
	site.noListing = false

	// A repair fetches the server and .NET again.
	before := site.archives.Load()
	calls := dotnet.calls.Load()
	if err := srv.Installer().Install(t.Context(), true, nil); err != nil {
		t.Fatal(err)
	}
	if site.archives.Load() != before+1 || dotnet.calls.Load() != calls+1 {
		t.Errorf("a repair downloaded %d archives and %d .NETs", site.archives.Load()-before, dotnet.calls.Load()-calls)
	}
	if b, ok := srv.Build(); !ok || b.Version != "1.23.0" {
		t.Errorf("after the repair Build() = %+v, %v", b, ok)
	}
}

func TestDotnetChannel(t *testing.T) {
	dir := t.TempDir()
	if _, err := dotnetChannel(dir); err == nil {
		t.Error("no runtimeconfig.json: no error")
	}
	writeFile(t, filepath.Join(dir, "VintagestoryServer.runtimeconfig.json"), readFile(t, filepath.Join("testdata", "runtimeconfig-1.22.7.json")))
	if c, err := dotnetChannel(dir); err != nil || c != "10.0" {
		t.Errorf("the real 1.22.7 file: %q, %v", c, err)
	}
	writeFile(t, filepath.Join(dir, "VintagestoryServer.runtimeconfig.json"),
		"\ufeff"+`{"runtimeOptions":{"frameworks":[{"name":"Microsoft.WindowsDesktop.App","version":"9.0.0"},{"name":"Microsoft.NETCore.App","version":"11.0.0-preview.1"}]}}`)
	if c, err := dotnetChannel(dir); err != nil || c != "11.0" {
		t.Errorf("a list of frameworks: %q, %v", c, err)
	}
}
