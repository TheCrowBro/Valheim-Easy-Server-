package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// Runtimes go into the instance's own Dirs.Runtime and are never shared, so
// FindByExe on the runtime's path finds only this instance's server. Each returns the
// path it found by searching the unpacked folder for bin/java[.exe] (on macOS
// Temurin's is under <jdk>/Contents/Home/bin/java) or dotnet[.exe]: callers use the
// returned path, never a guessed one.
//
// Layout inside dir: each version in its own folder ("temurin-jdk-25.0.5.1+2",
// "dotnet-10.0.15"), unpacked as the archive has it, plus a small note
// (.egs-java.json, .egs-dotnet.json) naming the one installed last, which
// FindJava and FindDotNet read. An install of the version that is already
// there downloads nothing (so a repair that wants a fresh copy removes dir
// first). A new version replaces the old ones, which are removed when
// nothing holds them open.

// Where the runtimes' metadata comes from.
const (
	adoptiumAPI    = "https://api.adoptium.net"
	dotnetMetadata = "https://builds.dotnet.microsoft.com/dotnet/release-metadata"
)

// Temurin installs Adoptium's Java runtime of the given major version into dir.
//
// It asks Adoptium's API for the newest Eclipse Temurin JRE of that major
// version for this computer (/v3/assets/latest/<major>/hotspot), which gives
// the archive's link and its SHA-256 in one answer, so the checksum always
// belongs to the file downloaded.
func Temurin(ctx context.Context, major int, dir string, step func(game.Progress)) (java string, err error) {
	return temurin(ctx, major, dir, runtime.GOOS, runtime.GOARCH, step)
}

// adoptiumAsset is the part of Adoptium's answer the app uses.
type adoptiumAsset struct {
	Binary struct {
		Architecture string `json:"architecture"`
		ImageType    string `json:"image_type"`
		JVMImpl      string `json:"jvm_impl"`
		OS           string `json:"os"`
		Package      struct {
			Checksum string `json:"checksum"`
			Link     string `json:"link"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
	ReleaseName string `json:"release_name"`
	Version     struct {
		Major          int    `json:"major"`
		OpenJDKVersion string `json:"openjdk_version"`
	} `json:"version"`
}

func temurin(ctx context.Context, major int, dir, goos, goarch string, step func(game.Progress)) (string, error) {
	if step == nil {
		step = func(game.Progress) {}
	}
	if major <= 0 {
		return "", fmt.Errorf("Java %d isn't a Java version", major)
	}
	what := "Java " + strconv.Itoa(major)
	osName := map[string]string{"windows": "windows", "darwin": "mac", "linux": "linux"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "aarch64"}[goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("Eclipse Temurin has no %s for this computer (%s/%s).", what, goos, goarch)
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}

	step(game.Progress{Step: "Looking for the newest " + what + "…", Percent: -1})
	q := url.Values{"architecture": {arch}, "image_type": {"jre"}, "os": {osName}, "vendor": {"eclipse"}}
	link := fmt.Sprintf("%s/v3/assets/latest/%d/hotspot?%s", adoptiumAPI, major, q.Encode())
	var assets []adoptiumAsset
	if err := getJSON(ctx, link, "Eclipse Temurin", &assets); err != nil {
		return "", err
	}
	var pick *adoptiumAsset
	for i, a := range assets {
		b := a.Binary
		if b.OS == osName && b.Architecture == arch && b.ImageType == "jre" && b.JVMImpl == "hotspot" &&
			a.Version.Major == major && b.Package.Link != "" && strings.HasSuffix(b.Package.Name, ext) {
			pick = &assets[i]
			break
		}
	}
	if pick == nil {
		return "", fmt.Errorf("Eclipse Temurin has no %s for this computer (%s %s).", what, osName, arch)
	}
	if pick.Binary.Package.Checksum == "" {
		return "", fmt.Errorf("Eclipse Temurin didn't publish a checksum for %s, so the app won't install it.", pick.Binary.Package.Name)
	}
	return installRuntime(ctx, runtimeInstall{
		dir:     dir,
		kind:    "temurin",
		note:    ".egs-java.json",
		version: pick.ReleaseName,
		what:    what,
		url:     pick.Binary.Package.Link,
		archive: pick.Binary.Package.Name,
		hash:    Hash{Algo: "sha256", Hex: pick.Binary.Package.Checksum},
		find:    func(root string) (string, bool) { return findExe(root, exeName("java", goos), true) },
	}, step)
}

// DotNet installs Microsoft's .NET runtime of the given channel ("8.0") into dir.
//
// It reads the channel's release metadata
// (builds.dotnet.microsoft.com/dotnet/release-metadata/<channel>/releases.json),
// takes its latest-runtime, and downloads the file named exactly
// dotnet-runtime-<rid>.zip (Windows) or .tar.gz (macOS, Linux), with its
// SHA-512: the plain runtime, not the installer, the apphost pack or ASP.NET.
func DotNet(ctx context.Context, channel string, dir string, step func(game.Progress)) (dotnet string, err error) {
	return dotNet(ctx, channel, dir, runtime.GOOS, runtime.GOARCH, step)
}

var reChannel = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)

// dotnetReleases is the part of a channel's releases.json the app uses.
type dotnetReleases struct {
	LatestRuntime string `json:"latest-runtime"`
	Releases      []struct {
		Runtime *struct {
			Version string `json:"version"`
			Files   []struct {
				Name string `json:"name"`
				RID  string `json:"rid"`
				URL  string `json:"url"`
				Hash string `json:"hash"`
			} `json:"files"`
		} `json:"runtime"`
	} `json:"releases"`
}

func dotNet(ctx context.Context, channel, dir, goos, goarch string, step func(game.Progress)) (string, error) {
	if step == nil {
		step = func(game.Progress) {}
	}
	if !reChannel.MatchString(channel) {
		return "", fmt.Errorf("%q isn't a .NET channel (like \"10.0\")", channel)
	}
	what := ".NET " + channel
	osName := map[string]string{"windows": "win", "darwin": "osx", "linux": "linux"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"}[goarch]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("Microsoft has no %s for this computer (%s/%s).", what, goos, goarch)
	}
	rid := osName + "-" + arch
	file := "dotnet-runtime-" + rid + ".tar.gz"
	if goos == "windows" {
		file = "dotnet-runtime-" + rid + ".zip"
	}

	step(game.Progress{Step: "Looking for the newest " + what + "…", Percent: -1})
	var meta dotnetReleases
	if err := getJSON(ctx, dotnetMetadata+"/"+channel+"/releases.json", "Microsoft's .NET site", &meta); err != nil {
		return "", err
	}
	if meta.LatestRuntime == "" {
		return "", fmt.Errorf("Microsoft's .NET site doesn't name a current %s runtime.", what)
	}
	var link, sum string
	for _, r := range meta.Releases {
		if r.Runtime == nil || r.Runtime.Version != meta.LatestRuntime {
			continue
		}
		for _, f := range r.Runtime.Files {
			if f.Name == file && f.RID == rid {
				link, sum = f.URL, f.Hash
			}
		}
		break
	}
	if link == "" {
		return "", fmt.Errorf("Microsoft's .NET site has no %s runtime %s for this computer (%s).", what, meta.LatestRuntime, rid)
	}
	if sum == "" {
		return "", fmt.Errorf("Microsoft didn't publish a checksum for %s, so the app won't install it.", file)
	}
	return installRuntime(ctx, runtimeInstall{
		dir:     dir,
		kind:    "dotnet",
		note:    ".egs-dotnet.json",
		version: meta.LatestRuntime,
		what:    what,
		url:     link,
		archive: file,
		hash:    Hash{Algo: "sha512", Hex: sum},
		find:    func(root string) (string, bool) { return findExe(root, exeName("dotnet", goos), false) },
	}, step)
}

// FindJava returns the java the last Temurin call installed in dir, if it is
// still there. Adapters use it for Program() without going online.
func FindJava(dir string) (string, bool) { return findNoted(dir, ".egs-java.json") }

// FindDotNet returns the dotnet the last DotNet call installed in dir, if it
// is still there.
func FindDotNet(dir string) (string, bool) { return findNoted(dir, ".egs-dotnet.json") }

// runtimeNote is what .egs-java.json and .egs-dotnet.json hold.
type runtimeNote struct {
	Version string `json:"version"`
	Path    string `json:"path"` // the program, relative to dir, slash-separated
}

func findNoted(dir, note string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, note))
	if err != nil {
		return "", false
	}
	var n runtimeNote
	if json.Unmarshal(b, &n) != nil || n.Path == "" || !filepath.IsLocal(filepath.FromSlash(n.Path)) {
		return "", false
	}
	abs, err := filepath.Abs(filepath.Join(dir, filepath.FromSlash(n.Path)))
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return abs, true
}

// runtimeInstall is one runtime version to put in dir.
type runtimeInstall struct {
	dir     string
	kind    string // folder prefix: "temurin", "dotnet"
	note    string
	version string
	what    string // "Java 25", ".NET 10.0"
	url     string
	archive string // the file's name, .zip or .tar.gz
	hash    Hash
	find    func(root string) (string, bool)
}

var reUnsafeChars = regexp.MustCompile(`[^A-Za-z0-9.+_-]`)

func installRuntime(ctx context.Context, r runtimeInstall, step func(game.Progress)) (string, error) {
	dir, err := filepath.Abs(r.dir)
	if err != nil {
		return "", err
	}
	if r.version == "" {
		return "", fmt.Errorf("the %s download has no version", r.what)
	}
	folder := r.kind + "-" + reUnsafeChars.ReplaceAllString(r.version, "_")
	final := filepath.Join(dir, folder)
	done := func(exe string) (string, error) {
		rel, err := filepath.Rel(dir, exe)
		if err != nil {
			return "", err
		}
		if err := writeJSON(filepath.Join(dir, r.note), runtimeNote{Version: r.version, Path: filepath.ToSlash(rel)}); err != nil {
			return "", err
		}
		removeOthers(dir, r.kind+"-", folder)
		step(game.Progress{Step: r.what + " is installed.", Percent: 100})
		return exe, nil
	}

	// This version is already here: nothing to download.
	if exe, ok := r.find(final); ok {
		return done(exe)
	}

	downloads := filepath.Join(dir, ".download")
	archive := filepath.Join(downloads, reUnsafeChars.ReplaceAllString(r.archive, "_"))
	defer func() {
		os.Remove(archive)
		os.Remove(downloads)
	}()
	text := "Downloading " + r.what + "…"
	err = Download(ctx, r.url, r.hash, archive, func(p game.Progress) {
		p.Step = text
		step(p)
	})
	if err != nil {
		return "", err
	}
	step(game.Progress{Step: "Unpacking " + r.what + "…", Percent: -1})
	partial := final + ".partial"
	os.RemoveAll(partial)
	if strings.HasSuffix(r.archive, ".zip") {
		err = Unzip(archive, partial, nil)
	} else {
		err = Untar(archive, partial)
	}
	if err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	if _, ok := r.find(partial); !ok {
		os.RemoveAll(partial)
		return "", fmt.Errorf("the %s download doesn't have the program the app looks for in it", r.what)
	}
	os.RemoveAll(final) // a broken earlier attempt
	if err := os.Rename(partial, final); err != nil {
		os.RemoveAll(partial)
		return "", err
	}
	exe, ok := r.find(final)
	if !ok {
		return "", fmt.Errorf("the %s download doesn't have the program the app looks for in it", r.what)
	}
	return done(exe)
}

// removeOthers removes older versions (folders starting with prefix other
// than keep) and leftovers of broken installs. A file still in use (a
// server running on the old Java, on Windows) keeps its folder until the
// next install; that does no harm.
func removeOthers(dir, prefix, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) && e.Name() != keep {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func exeName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// findExe searches root for the program called name, a few folders deep,
// and returns the one nearest the top (the one a runtime's own layout puts
// there: <jdk>/bin/java, <jdk>/Contents/Home/bin/java on macOS, or dotnet at
// the top). inBin asks for one whose folder is "bin".
func findExe(root, name string, inBin bool) (string, bool) {
	var found []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() {
			if depth >= 5 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.EqualFold(d.Name(), name) &&
			(!inBin || filepath.Base(filepath.Dir(p)) == "bin") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool {
		di := strings.Count(found[i], string(filepath.Separator))
		dj := strings.Count(found[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return found[i] < found[j]
	})
	return found[0], true
}

// getJSON reads a small JSON answer from a download site.
func getJSON(ctx context.Context, link, site string, v any) error {
	// A list is small: a site that hasn't answered in a minute won't.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("Couldn't reach %s. Check the internet connection. (%v)", site, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Couldn't get the download list from %s (it said %s).", site, resp.Status)
	}
	// Answers are a few kB (.NET's whole channel is about 1 MB).
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v); err != nil {
		return fmt.Errorf("%s answered something the app doesn't understand (%v).", site, err)
	}
	return nil
}

// writeJSON writes v to path in one piece (a temp file, then a rename), so a
// crash never leaves half a file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
