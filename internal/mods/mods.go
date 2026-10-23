// Package mods installs BepInEx mods for the Valheim server from
// Thunderstore, where Valheim's mod managers get them too, and switches the
// mod loader on or off before the server starts.
package mods

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Mod is a mod the app can turn on for the server.
type Mod struct {
	ID      string `json:"id"`      // what the settings store
	Package string `json:"package"` // Thunderstore name, Namespace-Name
	Name    string `json:"name"`
	Author  string `json:"author"`
	Summary string `json:"summary"`
	URL     string `json:"url"`
}

// Catalog lists the mods the app offers.
var Catalog = []Mod{{
	ID:      "advancedportals",
	Package: "RandyKnapp-AdvancedPortals",
	Name:    "Advanced Portals",
	Author:  "RandyKnapp",
	Summary: "Three new portals that let metal through: the Ancient Portal takes copper and tin, the Obsidian Portal iron, and the Black Marble Portal anything.",
	URL:     "https://thunderstore.io/c/valheim/p/RandyKnapp/AdvancedPortals/",
}}

// Find returns the catalog entry for id.
func Find(id string) (Mod, bool) {
	for _, m := range Catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Mod{}, false
}

// Loader is the BepInEx pack every mod needs. It goes into the server folder
// itself rather than into BepInEx/plugins.
const Loader = "denikson-BepInExPack_Valheim"

// DefaultURL is Thunderstore's address.
const DefaultURL = "https://thunderstore.io"

const (
	proxyDLL   = "winhttp.dll" // loads BepInEx when the server starts on Windows
	proxyOff   = "winhttp.dll.off"
	loaderRoot = "BepInExPack_Valheim/" // the pack's files, inside its zip
	recordFile = "valheim-easy-server.json"
	maxZip     = 200 << 20
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9_]+-[A-Za-z0-9_]+$`)

// Installer looks after the mods in one Valheim server folder.
type Installer struct {
	Dir    string       // the server folder
	URL    string       // Thunderstore; DefaultURL when empty
	Client *http.Client // one with a timeout when nil
}

// record is what this app installed, kept in the BepInEx folder.
type record struct {
	Packages map[string]installed `json:"packages"` // by Namespace-Name
}

type installed struct {
	Version string   `json:"version"`
	Deps    []string `json:"deps,omitempty"`
}

type pkg struct {
	Name, Version, URL string
	Deps               []string
}

// Result says what Prepare did.
type Result struct {
	Changed []string // a line for each package installed or updated
	Warning string   // set when updates couldn't be checked
}

// Installed returns the version of each package this app installed.
func (in *Installer) Installed() map[string]string {
	out := map[string]string{}
	for name, p := range in.read().Packages {
		out[name] = p.Version
	}
	return out
}

// Prepare gets the server folder ready to start with the mods in ids. It
// installs what's missing, updates everything when update is true, and
// turns the mod loader on. With no ids it turns the loader off again, if
// this app put it there.
func (in *Installer) Prepare(ctx context.Context, ids []string, update bool, step func(string)) (Result, error) {
	var res Result
	if step == nil {
		step = func(string) {}
	}
	rec := in.read()
	if len(ids) == 0 {
		if len(rec.Packages) == 0 {
			return res, nil // a loader installed by hand is left alone
		}
		return res, in.switchLoader(false)
	}
	want := []string{Loader}
	for _, id := range ids {
		m, ok := Find(id)
		if !ok {
			return res, fmt.Errorf("unknown mod %q", id)
		}
		want = append(want, m.Package)
	}
	if !update && in.complete(rec, want) {
		return res, in.switchLoader(true)
	}
	step("Checking the mods on Thunderstore…")
	pkgs, err := in.resolve(ctx, want)
	if err != nil {
		if ctx.Err() == nil && in.complete(rec, want) {
			res.Warning = fmt.Sprintf("Couldn't check for mod updates (%v). Starting with the installed versions.", err)
			return res, in.switchLoader(true)
		}
		return res, err
	}
	for _, p := range pkgs {
		if cur, ok := rec.Packages[p.Name]; ok && cur.Version == p.Version {
			continue
		}
		step(fmt.Sprintf("Downloading %s %s…", Title(p.Name), p.Version))
		if err := in.install(ctx, p); err != nil {
			return res, fmt.Errorf("couldn't install %s: %w", Title(p.Name), err)
		}
		rec.Packages[p.Name] = installed{Version: p.Version, Deps: p.Deps}
		if err := in.write(rec); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, fmt.Sprintf("Installed %s %s.", Title(p.Name), p.Version))
	}
	return res, in.switchLoader(true)
}

// Title is a package's name for people.
func Title(name string) string {
	for _, m := range Catalog {
		if m.Package == name {
			return m.Name
		}
	}
	switch name {
	case Loader:
		return "BepInEx"
	case "ValheimModding-Jotunn":
		return "Jötunn"
	}
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// Env is what the server's environment needs to load mods on Linux. On
// Windows, winhttp.dll in the server folder does the same job.
func Env(dir string, on bool) []string {
	if !on || runtime.GOOS != "linux" {
		return nil
	}
	return []string{
		"DOORSTOP_ENABLED=1",
		"DOORSTOP_TARGET_ASSEMBLY=" + filepath.Join(dir, "BepInEx", "core", "BepInEx.Preloader.dll"),
		"LD_LIBRARY_PATH=" + filepath.Join(dir, "doorstop_libs") + ":" + filepath.Join(dir, "linux64") + ":" + os.Getenv("LD_LIBRARY_PATH"),
		"LD_PRELOAD=libdoorstop_x64.so",
	}
}

// complete reports whether the packages in want and everything they need
// are installed and still on disk.
func (in *Installer) complete(r record, want []string) bool {
	seen := map[string]bool{}
	for queue := slices.Clone(want); len(queue) > 0; {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		p, ok := r.Packages[name]
		if !ok || !in.onDisk(name) {
			return false
		}
		queue = append(queue, p.Deps...)
	}
	return true
}

func (in *Installer) onDisk(name string) bool {
	exists := func(rel ...string) bool {
		_, err := os.Stat(filepath.Join(append([]string{in.Dir}, rel...)...))
		return err == nil
	}
	if name == Loader {
		return exists("BepInEx", "core", "BepInEx.Preloader.dll") && (exists(proxyDLL) || exists(proxyOff))
	}
	return exists("BepInEx", "plugins", name)
}

// resolve finds the latest version of each package in want and of
// everything they need, the loader first and libraries before the mods
// that use them.
func (in *Installer) resolve(ctx context.Context, want []string) ([]pkg, error) {
	var found []pkg
	seen := map[string]bool{}
	for queue := slices.Clone(want); len(queue) > 0; {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		p, err := in.latest(ctx, name)
		if err != nil {
			return nil, err
		}
		found = append(found, p)
		queue = append(queue, p.Deps...)
	}
	slices.Reverse(found)
	slices.SortStableFunc(found, func(a, b pkg) int {
		switch {
		case a.Name == Loader && b.Name != Loader:
			return -1
		case b.Name == Loader && a.Name != Loader:
			return 1
		}
		return 0
	})
	return found, nil
}

func (in *Installer) latest(ctx context.Context, name string) (pkg, error) {
	if !safeName.MatchString(name) {
		return pkg{}, fmt.Errorf("unexpected package name %q", name)
	}
	ns, short, _ := strings.Cut(name, "-")
	var body struct {
		Latest struct {
			Version  string   `json:"version_number"`
			Download string   `json:"download_url"`
			Deps     []string `json:"dependencies"`
		} `json:"latest"`
	}
	u := fmt.Sprintf("%s/api/experimental/package/%s/%s/", in.base(), url.PathEscape(ns), url.PathEscape(short))
	if err := in.getJSON(ctx, u, &body); err != nil {
		return pkg{}, err
	}
	p := pkg{Name: name, Version: body.Latest.Version, URL: body.Latest.Download}
	if p.Version == "" || p.URL == "" {
		return pkg{}, fmt.Errorf("Thunderstore has no download for %s", Title(name))
	}
	for _, d := range body.Latest.Deps {
		if i := strings.LastIndex(d, "-"); i > 0 {
			d = d[:i] // drop the version: Namespace-Name-1.2.3
		}
		p.Deps = append(p.Deps, d)
	}
	return p, nil
}

func (in *Installer) base() string {
	if in.URL != "" {
		return strings.TrimRight(in.URL, "/")
	}
	return DefaultURL
}

func (in *Installer) client() *http.Client {
	if in.Client != nil {
		return in.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (in *Installer) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ValheimEasyServer")
	res, err := in.client().Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach Thunderstore. Is the internet connection working?")
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("Thunderstore answered %s", res.Status)
	}
	return res, nil
}

func (in *Installer) getJSON(ctx context.Context, u string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := in.get(ctx, u)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(v)
}

// install downloads a package and unpacks it into the server folder.
func (in *Installer) install(ctx context.Context, p pkg) error {
	if u, err := url.Parse(p.URL); err != nil || !strings.HasPrefix(p.URL, in.base()+"/") || u.User != nil {
		return fmt.Errorf("unexpected download address %q", p.URL)
	}
	res, err := in.get(ctx, p.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp("", "ves-mod-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(res.Body, maxZip+1))
	if err != nil {
		return fmt.Errorf("the download broke off: %v", err)
	}
	if n > maxZip {
		return errors.New("the download is too big")
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return fmt.Errorf("the download isn't a valid zip file: %v", err)
	}
	if p.Name == Loader {
		return in.unpackLoader(zr)
	}
	return in.unpackPlugin(zr, p.Name)
}

// unpackLoader puts the BepInEx pack into the server folder, keeping an
// existing BepInEx.cfg.
func (in *Installer) unpackLoader(zr *zip.Reader) error {
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		rel, ok := strings.CutPrefix(name, loaderRoot)
		if !ok || rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		if unsafeName(rel) {
			return fmt.Errorf("the download has a file with an unsafe name (%q)", f.Name)
		}
		if err := in.extract(f, rel, rel == "BepInEx/config/BepInEx.cfg"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "BepInEx", "core", "BepInEx.Preloader.dll")); err != nil {
		return errors.New("the BepInEx download didn't contain BepInEx")
	}
	return nil
}

// unpackPlugin puts a mod into BepInEx/plugins/<package>, replacing an older
// copy, the way the mod managers lay it out.
func (in *Installer) unpackPlugin(zr *zip.Reader, name string) error {
	for _, dir := range []string{"plugins", "patchers"} {
		if err := os.RemoveAll(filepath.Join(in.Dir, "BepInEx", dir, name)); err != nil {
			return err
		}
	}
	for _, f := range zr.File {
		file := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasSuffix(file, "/") {
			continue
		}
		switch strings.ToLower(file) {
		case "manifest.json", "readme.md", "changelog.md", "icon.png", "license", "license.md", "license.txt":
			continue
		}
		if unsafeName(file) {
			return fmt.Errorf("the download has a file with an unsafe name (%q)", f.Name)
		}
		rel, keep := path.Join("BepInEx/plugins", name, file), false
		if r, ok := strings.CutPrefix(file, "plugins/"); ok {
			rel = path.Join("BepInEx/plugins", name, r)
		} else if r, ok := strings.CutPrefix(file, "patchers/"); ok {
			rel = path.Join("BepInEx/patchers", name, r)
		} else if r, ok := strings.CutPrefix(file, "config/"); ok {
			rel, keep = path.Join("BepInEx/config", r), true // settings someone changed stay
		}
		if err := in.extract(f, rel, keep); err != nil {
			return err
		}
	}
	return nil
}

// unsafeName reports whether a file name from a zip could point outside the
// folder it's unpacked into.
func unsafeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return true
	}
	return slices.Contains(strings.Split(name, "/"), "..")
}

// extract writes one file from a zip to rel inside the server folder. keep
// leaves a file that's already there alone.
func (in *Installer) extract(f *zip.File, rel string, keep bool) error {
	clean := path.Clean("/" + rel)[1:]
	if clean == "" || clean != rel || strings.Contains(rel, ":") || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("the download has a file with an unsafe name (%q)", f.Name)
	}
	dst := filepath.Join(in.Dir, filepath.FromSlash(clean))
	if keep {
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := dst + ".ves-new"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(rc, maxZip))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// switchLoader turns BepInEx on or off for the next start by putting
// winhttp.dll in place or moving it aside.
func (in *Installer) switchLoader(on bool) error {
	dll, off := filepath.Join(in.Dir, proxyDLL), filepath.Join(in.Dir, proxyOff)
	_, errDLL := os.Stat(dll)
	if !on {
		if errDLL == nil {
			return os.Rename(dll, off)
		}
		return nil
	}
	if errDLL == nil {
		os.Remove(off)
	} else if err := os.Rename(off, dll); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return in.quietConsole()
}

// quietConsole turns off BepInEx's console window. The server runs without
// a window, and closing that console would stop it.
func (in *Installer) quietConsole() error {
	p := filepath.Join(in.Dir, "BepInEx", "config", "BepInEx.cfg")
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil // BepInEx writes one with the console off
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	section, changed := "", false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = t
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if section == "[Logging.Console]" && ok && strings.TrimSpace(k) == "Enabled" && strings.TrimSpace(v) != "false" {
			lines[i] = "Enabled = false"
			if strings.HasSuffix(l, "\r") {
				lines[i] += "\r"
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
}

func (in *Installer) read() record {
	r := record{Packages: map[string]installed{}}
	b, err := os.ReadFile(filepath.Join(in.Dir, "BepInEx", recordFile))
	if err == nil {
		json.Unmarshal(b, &r)
	}
	if r.Packages == nil {
		r.Packages = map[string]installed{}
	}
	return r
}

func (in *Installer) write(r record) error {
	p := filepath.Join(in.Dir, "BepInEx", recordFile)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
