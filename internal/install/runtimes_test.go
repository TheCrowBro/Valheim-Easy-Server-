package install

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"valheim-easy-server/internal/game"
)

// The recorded answers in testdata were fetched on 2026-11-26:
//
//	adoptium-25-<os>-<arch>.json  https://api.adoptium.net/v3/assets/latest/25/hotspot?architecture=<arch>&image_type=jre&os=<os>&vendor=eclipse
//	                              (windows-aarch64 really is an empty list: there is no such JRE)
//	dotnet-10.0-releases.json     https://builds.dotnet.microsoft.com/dotnet/release-metadata/10.0/releases.json,
//	                              trimmed to its two newest releases' runtime files (and some ASP.NET
//	                              files, which must not be picked); every kept field is as served
//
// The real archives are tens of MB, so the tests serve small ones with the
// same layout (checked against the real 25.0.5.1+2 macOS JRE and the .NET
// 10.0.15 runtimes) and put their checksums into the recorded answers. One
// test keeps the real checksum, to show it is checked.

// site stands in for the internet: the recorded answers, and one archive
// at the exact link the answer gives. Any other request fails the test.
type site struct {
	t        *testing.T
	answers  map[string]func(r *http.Request) []byte // by host and escaped path
	mu       sync.Mutex
	requests []string
}

func newSite(t *testing.T) *site {
	s := &site{t: t, answers: map[string]func(*http.Request) []byte{}}
	answerFrom(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.URL.String())
		s.mu.Unlock()
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("no User-Agent on %s", r.URL)
		}
		answer, ok := s.answers[r.URL.Host+r.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected request for %s", r.URL)
			http.NotFound(w, r)
			return
		}
		w.Write(answer(r))
	})
	return s
}

func (s *site) serve(link string, answer func(r *http.Request) []byte) {
	s.t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		s.t.Fatal(err)
	}
	s.answers[u.Host+u.EscapedPath()] = answer
}

func (s *site) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func recorded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// adoptium serves the recorded answer for osName/arch, with the package's
// checksum set to the archive's (unless realSum), and the archive at the
// answer's link. It returns that link.
func (s *site) adoptium(osName, arch string, archive []byte, realSum bool) string {
	s.t.Helper()
	var assets []map[string]any
	if err := json.Unmarshal(recorded(s.t, "adoptium-25-"+osName+"-"+arch+".json"), &assets); err != nil {
		s.t.Fatal(err)
	}
	link := ""
	if len(assets) > 0 {
		pkg := assets[0]["binary"].(map[string]any)["package"].(map[string]any)
		link = pkg["link"].(string)
		if !realSum {
			sum := sha256.Sum256(archive)
			pkg["checksum"] = hex.EncodeToString(sum[:])
		}
		s.serve(link, func(*http.Request) []byte { return archive })
	}
	answer, err := json.Marshal(assets)
	if err != nil {
		s.t.Fatal(err)
	}
	s.serve(adoptiumAPI+"/v3/assets/latest/25/hotspot", func(r *http.Request) []byte {
		q := r.URL.Query()
		if q.Get("os") != osName || q.Get("architecture") != arch || q.Get("image_type") != "jre" || q.Get("vendor") != "eclipse" {
			s.t.Errorf("Adoptium was asked %s", r.URL.RawQuery)
		}
		return answer
	})
	return link
}

// jreLayout is the real Temurin JRE's layout, cut down: on macOS the
// program is under Contents/Home.
func jreLayout(goos string) []entry {
	top := "jdk-25.0.5.1+2-jre/"
	switch goos {
	case "darwin":
		return []entry{
			{name: top, mode: fs.ModeDir},
			{name: top + "Contents/Info.plist", body: "plist"},
			{name: top + "Contents/MacOS/libjli.dylib", body: "jli"},
			{name: top + "Contents/Home/bin/java", body: "java", mode: 0o755},
			{name: top + "Contents/Home/bin/keytool", body: "keytool", mode: 0o755},
			{name: top + "Contents/Home/lib/libjli.dylib", body: "jli"},
			{name: top + "Contents/Home/lib/security/cacerts", body: "certs", mode: 0o444},
			{name: top + "Contents/Home/legal/java.base/LICENSE", body: "GPL", mode: 0o444},
		}
	case "windows":
		return []entry{
			{name: top, mode: fs.ModeDir | 0o755},
			{name: top + "bin/java.exe", body: "java"},
			{name: top + "bin/javaw.exe", body: "javaw"},
			{name: top + "lib/modules", body: "modules"},
		}
	}
	return []entry{
		{name: top, mode: fs.ModeDir},
		{name: top + "bin/java", body: "java", mode: 0o755},
		{name: top + "lib/server/libjvm.so", body: "jvm"},
	}
}

func TestTemurin(t *testing.T) {
	for _, c := range []struct {
		goos, goarch, osName, arch string
		zip                        bool
		want                       string // the program, relative to the runtime folder
	}{
		{"darwin", "amd64", "mac", "x64", false, "temurin-jdk-25.0.5.1+2/jdk-25.0.5.1+2-jre/Contents/Home/bin/java"},
		{"darwin", "arm64", "mac", "aarch64", false, "temurin-jdk-25.0.5.1+2/jdk-25.0.5.1+2-jre/Contents/Home/bin/java"},
		{"windows", "amd64", "windows", "x64", true, "temurin-jdk-25.0.5.1+2/jdk-25.0.5.1+2-jre/bin/java.exe"},
		{"linux", "amd64", "linux", "x64", false, "temurin-jdk-25.0.5.1+2/jdk-25.0.5.1+2-jre/bin/java"},
	} {
		t.Run(c.goos+"-"+c.goarch, func(t *testing.T) {
			s := newSite(t)
			var archive []byte
			if c.zip {
				archive = makeZip(t, jreLayout(c.goos))
			} else {
				archive = makeTarGz(t, jreLayout(c.goos))
			}
			link := s.adoptium(c.osName, c.arch, archive, false)
			dir := filepath.Join(t.TempDir(), "runtime")
			var steps []string
			java, err := temurin(context.Background(), 25, dir, c.goos, c.goarch, func(p game.Progress) { steps = append(steps, p.Step) })
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dir, filepath.FromSlash(c.want)); java != want {
				t.Errorf("java = %s, want %s", java, want)
			}
			if !filepath.IsAbs(java) {
				t.Errorf("java = %s, not a full path", java)
			}
			if !c.zip {
				runnable(t, java, true)
			}
			if got, ok := FindJava(dir); !ok || got != java {
				t.Errorf("FindJava = %s, %v", got, ok)
			}
			if s.count(link) != 1 {
				t.Errorf("the archive was fetched %d times", s.count(link))
			}
			for _, want := range []string{"Looking for the newest Java 25…", "Downloading Java 25…", "Unpacking Java 25…", "Java 25 is installed."} {
				if !strings.Contains(strings.Join(steps, "\n"), want) {
					t.Errorf("progress %q lacks %q", steps, want)
				}
			}
			// Nothing but the runtime and its note is left behind.
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if strings.Join(names, ",") != ".egs-java.json,temurin-jdk-25.0.5.1+2" {
				t.Errorf("the runtime folder has %q", names)
			}

			// Installing again finds it there and downloads nothing.
			again, err := temurin(context.Background(), 25, dir, c.goos, c.goarch, nil)
			if err != nil || again != java {
				t.Errorf("second Temurin = %s, %v", again, err)
			}
			if s.count(link) != 1 {
				t.Error("the same version was downloaded again")
			}
		})
	}
}

// The published checksum is really checked: the recorded answer's own
// SHA-256 (of the real 42 MB archive) doesn't match the stand-in archive.
func TestTemurinChecksum(t *testing.T) {
	s := newSite(t)
	s.adoptium("mac", "x64", makeTarGz(t, jreLayout("darwin")), true)
	dir := filepath.Join(t.TempDir(), "runtime")
	_, err := temurin(context.Background(), 25, dir, "darwin", "amd64", nil)
	if !errors.Is(err, errChecksum) {
		t.Fatalf("Temurin = %v, want a checksum error", err)
	}
	if _, ok := FindJava(dir); ok {
		t.Error("a Java whose checksum failed was installed")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestTemurinNotForThisComputer(t *testing.T) {
	s := newSite(t)
	s.adoptium("windows", "aarch64", nil, false) // recorded: []
	_, err := temurin(context.Background(), 25, t.TempDir(), "windows", "arm64", nil)
	if err == nil || err.Error() != "Eclipse Temurin has no Java 25 for this computer (windows aarch64)." {
		t.Errorf("Temurin = %v", err)
	}
	if _, err := temurin(context.Background(), 25, t.TempDir(), "freebsd", "amd64", nil); err == nil {
		t.Error("Temurin for FreeBSD worked")
	}
	if _, err := temurin(context.Background(), 0, t.TempDir(), "linux", "amd64", nil); err == nil {
		t.Error("Java 0 worked")
	}
}

// A JRE without bin/java isn't installed, and a new version replaces the
// old one.
func TestTemurinLayoutAndUpdate(t *testing.T) {
	s := newSite(t)
	s.adoptium("linux", "x64", makeTarGz(t, []entry{{name: "jdk/lib/modules", body: "x"}}), false)
	dir := filepath.Join(t.TempDir(), "runtime")
	if _, err := temurin(context.Background(), 25, dir, "linux", "amd64", nil); err == nil || !strings.Contains(err.Error(), "doesn't have the program") {
		t.Errorf("Temurin without bin/java = %v", err)
	}

	// An older version is installed ...
	old := filepath.Join(dir, "temurin-jdk-25.0.4+7", "jdk-25.0.4+7-jre", "bin")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, "java"), []byte("old"), 0o755)
	// ... and the newest one comes.
	s.adoptium("linux", "x64", makeTarGz(t, jreLayout("linux")), false)
	java, err := temurin(context.Background(), 25, dir, "linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(java, "temurin-jdk-25.0.5.1+2") {
		t.Errorf("java = %s", java)
	}
	if _, err := os.Stat(filepath.Join(dir, "temurin-jdk-25.0.4+7")); !os.IsNotExist(err) {
		t.Error("the old version is still there")
	}
}

func TestTemurinOffline(t *testing.T) {
	old := client
	client = &http.Client{Transport: failingTransport{}}
	defer func() { client = old }()
	_, err := temurin(context.Background(), 25, t.TempDir(), "windows", "amd64", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "Couldn't reach Eclipse Temurin. Check the internet connection.") {
		t.Errorf("Temurin offline = %v", err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

// dotnetSite serves the recorded .NET 10.0 metadata with file's sha512 set
// to the archive's, and the archive at the link the metadata gives.
func (s *site) dotnet(file string, archive []byte) string {
	s.t.Helper()
	var meta map[string]any
	if err := json.Unmarshal(recorded(s.t, "dotnet-10.0-releases.json"), &meta); err != nil {
		s.t.Fatal(err)
	}
	link := ""
	for _, rel := range meta["releases"].([]any) {
		runtime := rel.(map[string]any)["runtime"].(map[string]any)
		if runtime["version"] != meta["latest-runtime"] {
			continue
		}
		for _, f := range runtime["files"].([]any) {
			f := f.(map[string]any)
			if f["name"] == file {
				sum := sha512.Sum512(archive)
				f["hash"] = hex.EncodeToString(sum[:])
				link = f["url"].(string)
			}
		}
	}
	if link == "" {
		s.t.Fatalf("the recorded metadata has no %s", file)
	}
	s.serve(link, func(*http.Request) []byte { return archive })
	answer, _ := json.Marshal(meta)
	s.serve(dotnetMetadata+"/10.0/releases.json", func(*http.Request) []byte { return answer })
	return link
}

func TestDotNet(t *testing.T) {
	for _, c := range []struct {
		goos, goarch, file, link, want string
	}{
		{"windows", "amd64", "dotnet-runtime-win-x64.zip",
			"https://builds.dotnet.microsoft.com/dotnet/Runtime/10.0.15/dotnet-runtime-10.0.15-win-x64.zip", "dotnet-10.0.15/dotnet.exe"},
		{"darwin", "amd64", "dotnet-runtime-osx-x64.tar.gz",
			"https://builds.dotnet.microsoft.com/dotnet/Runtime/10.0.15/dotnet-runtime-10.0.15-osx-x64.tar.gz", "dotnet-10.0.15/dotnet"},
		{"linux", "arm64", "dotnet-runtime-linux-arm64.tar.gz",
			"https://builds.dotnet.microsoft.com/dotnet/Runtime/10.0.15/dotnet-runtime-10.0.15-linux-arm64.tar.gz", "dotnet-10.0.15/dotnet"},
	} {
		t.Run(c.goos+"-"+c.goarch, func(t *testing.T) {
			s := newSite(t)
			var archive []byte
			if c.goos == "windows" {
				// The real zip: dotnet.exe at the top, no Unix modes.
				archive = makeZip(t, []entry{
					{name: "dotnet.exe", body: "host"},
					{name: "host/fxr/10.0.15/hostfxr.dll", body: "fxr"},
					{name: "shared/Microsoft.NETCore.App/10.0.15/System.Private.CoreLib.dll", body: "corelib"},
				})
			} else {
				// The real tarball: every name starts with "./".
				archive = makeTarGz(t, []entry{
					{name: "./", mode: fs.ModeDir},
					{name: "./host/", mode: fs.ModeDir},
					{name: "./dotnet", body: "host", mode: 0o755},
					{name: "./host/fxr/10.0.15/libhostfxr.dylib", body: "fxr"},
					{name: "./shared/Microsoft.NETCore.App/10.0.15/createdump", body: "dump", mode: 0o755},
				})
			}
			link := s.dotnet(c.file, archive)
			if link != c.link {
				t.Fatalf("the recorded link is %s", link)
			}
			dir := filepath.Join(t.TempDir(), "runtime")
			dotnet, err := dotNet(context.Background(), "10.0", dir, c.goos, c.goarch, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dir, filepath.FromSlash(c.want)); dotnet != want {
				t.Errorf("dotnet = %s, want %s", dotnet, want)
			}
			if c.goos != "windows" {
				runnable(t, dotnet, true)
			}
			if got, ok := FindDotNet(dir); !ok || got != dotnet {
				t.Errorf("FindDotNet = %s, %v", got, ok)
			}
			if s.count(c.link) != 1 {
				t.Errorf("the runtime was fetched %d times", s.count(c.link))
			}
			if _, err := dotNet(context.Background(), "10.0", dir, c.goos, c.goarch, nil); err != nil || s.count(c.link) != 1 {
				t.Errorf("a second install downloaded again (%v)", err)
			}
		})
	}
}

func TestDotNetProblems(t *testing.T) {
	for _, ch := range []string{"10", "10.0/../x", "", "latest"} {
		if _, err := dotNet(context.Background(), ch, t.TempDir(), "windows", "amd64", nil); err == nil {
			t.Errorf("channel %q worked", ch)
		}
	}
	s := newSite(t)
	s.dotnet("dotnet-runtime-win-x64.zip", makeZip(t, []entry{{name: "dotnet.exe", body: "host"}}))
	// The recorded metadata has no runtime for 32-bit Linux.
	_, err := dotNet(context.Background(), "10.0", t.TempDir(), "linux", "386", nil)
	if err == nil || err.Error() != "Microsoft's .NET site has no .NET 10.0 runtime 10.0.15 for this computer (linux-x86)." {
		t.Errorf(".NET for 32-bit Linux = %v", err)
	}
	// A zip without dotnet.exe isn't installed.
	s.dotnet("dotnet-runtime-win-x64.zip", makeZip(t, []entry{{name: "LICENSE.txt", body: "MIT"}}))
	if _, err := dotNet(context.Background(), "10.0", t.TempDir(), "windows", "amd64", nil); err == nil {
		t.Error("a .NET without dotnet.exe was installed")
	}
}

func TestFindRuntimeWithoutNote(t *testing.T) {
	dir := t.TempDir()
	if _, ok := FindJava(dir); ok {
		t.Error("FindJava found something in an empty folder")
	}
	os.WriteFile(filepath.Join(dir, ".egs-dotnet.json"), []byte(`{"version":"10.0.15","path":"../outside/dotnet"}`), 0o644)
	if _, ok := FindDotNet(dir); ok {
		t.Error("FindDotNet followed a note out of its folder")
	}
	os.WriteFile(filepath.Join(dir, ".egs-dotnet.json"), []byte(`{"version":"10.0.15","path":"dotnet-10.0.15/dotnet"}`), 0o644)
	if _, ok := FindDotNet(dir); ok {
		t.Error("FindDotNet found a program that isn't there")
	}
}
