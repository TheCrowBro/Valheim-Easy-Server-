package worldstore

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// env is a pretend data folder: the live worlds, the app's backups and
// uploads, a folder downloads go to, and a home folder where the owner's
// own copy of the game keeps worlds.
type env struct {
	root, live, backups, staging, export, home, saves string
}

func newEnv(t *testing.T) env {
	t.Helper()
	root := t.TempDir()
	e := env{
		root:    root,
		live:    filepath.Join(root, "games", "demo", "saves"),
		backups: filepath.Join(root, "games", "demo", "backups"),
		staging: filepath.Join(root, "games", "demo", "uploads"),
		export:  filepath.Join(root, "downloads"),
		home:    filepath.Join(root, "home"),
	}
	e.saves = filepath.Join(e.home, "game", "saves")
	for _, d := range []string{e.live, e.export, e.saves} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", e.home)        // os.UserHomeDir on macOS and Linux
	t.Setenv("USERPROFILE", e.home) // and on Windows
	return e
}

// layout is one of the two world layouts, with what a test needs to make
// and look at its worlds.
type layout struct {
	kind   string
	format func(e env) Format
	make   func(t *testing.T, dir, name, seed string)
	// paths lists a world's files relative to its folder of worlds.
	paths func(name string) []string
}

var layouts = []layout{
	{
		kind: KindFolder,
		format: func(e env) Format {
			return Format{Dir: e.live, Kind: KindFolder, MaxName: 40,
				LocalIn: func(home string) []string { return []string{filepath.Join(home, "game", "saves")} }}
		},
		make: func(t *testing.T, dir, name, seed string) {
			w := filepath.Join(dir, name)
			writeFile(t, filepath.Join(w, "level.dat"), "seed="+seed)
			writeFile(t, filepath.Join(w, "region", "r.0.0.mca"), strings.Repeat("r", 3000))
			writeFile(t, filepath.Join(w, "data", "raids.dat"), "raids "+seed)
			if err := os.MkdirAll(filepath.Join(w, "datapacks"), 0o755); err != nil { // empty, as Minecraft leaves it
				t.Fatal(err)
			}
		},
		paths: func(name string) []string {
			return []string{name + "/", name + "/data/", name + "/data/raids.dat", name + "/datapacks/",
				name + "/level.dat", name + "/region/", name + "/region/r.0.0.mca"}
		},
	},
	{
		kind: KindFiles,
		format: func(e env) Format {
			return Format{Dir: e.live, Kind: KindFiles, Ext: []string{".wld", ".wld.bak"}, MaxName: 40,
				LocalIn: func(home string) []string { return []string{filepath.Join(home, "game", "saves")} }}
		},
		make: func(t *testing.T, dir, name, seed string) {
			writeFile(t, filepath.Join(dir, name+".wld"), "world "+seed+strings.Repeat("w", 2000))
			writeFile(t, filepath.Join(dir, name+".wld.bak"), "previous "+seed)
		},
		paths: func(name string) []string { return []string{name + ".wld", name + ".wld.bak"} },
	},
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tree reads everything under dir: slash path -> contents, "/" at the end
// of a folder's path. Missing dir reads as empty.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = string(data)
		return nil
	})
	return out
}

// world reads one world's files, relative to its own name, so two worlds
// (or one before and after a rename) can be compared.
func world(t *testing.T, l layout, dir, name string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, data := range tree(t, dir) {
		for _, want := range l.paths(name) {
			if p == want {
				out[strings.Replace(p, name, "W", 1)] = data
			}
		}
	}
	return out
}

// worldFiles is world without its folders.
func worldFiles(t *testing.T, l layout, dir, name string) map[string]string {
	t.Helper()
	out := world(t, l, dir, name)
	for k := range out {
		if strings.HasSuffix(k, "/") {
			delete(out, k)
		}
	}
	return out
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// zipNames lists a zip's entries.
func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// buildZip writes a zip of files (name -> contents) at path.
func buildZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	buildZipAt(t, path, time.Now(), files)
}

// buildZipAt is buildZip with every entry saved at mod.
func buildZipAt(t *testing.T, path string, mod time.Time, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range keys(files) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[name]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, buf.String())
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// noHidden fails if a change left one of its hidden folders in Dir.
func noHidden(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".egs-") {
			t.Fatalf("%s left in the worlds folder", e.Name())
		}
	}
}

// backupsOf lists a world's backup reasons, newest first.
func backupsOf(t *testing.T, w game.Worlds, name string) []string {
	t.Helper()
	list, orphans, err := w.List()
	if err != nil {
		t.Fatal(err)
	}
	var bs []worlds.Backup
	for _, x := range list {
		if x.Name == name {
			bs = x.Backups
		}
	}
	for _, o := range orphans {
		if o.Name == name {
			bs = o.Backups
		}
	}
	var out []string
	for _, b := range bs {
		_, reason, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(b.ID, "app:"), ".zip"), "_")
		out = append(out, reason)
	}
	return out
}

func findBackup(t *testing.T, w game.Worlds, name, reason string) worlds.Backup {
	t.Helper()
	list, orphans, err := w.List()
	if err != nil {
		t.Fatal(err)
	}
	var bs []worlds.Backup
	for _, x := range list {
		if x.Name == name {
			bs = x.Backups
		}
	}
	for _, o := range orphans {
		if o.Name == name {
			bs = o.Backups
		}
	}
	for _, b := range bs {
		if strings.HasSuffix(b.ID, "_"+reason+".zip") {
			return b
		}
	}
	t.Fatalf("%s has no %q backup (it has %v)", name, reason, backupsOf(t, w, name))
	return worlds.Backup{}
}

func never(string) bool { return false }

func importAll(cands []worlds.Candidate, action string) []worlds.Choice {
	var ch []worlds.Choice
	for _, c := range cands {
		a := action
		if c.Superseded {
			a = "skip"
		}
		ch = append(ch, worlds.Choice{ID: c.ID, Action: a})
	}
	return ch
}

// outside reads everything under root that isn't one of the store's
// folders (or the downloads folder an export is told to use).
func outside(t *testing.T, e env) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, data := range tree(t, e.root) {
		full := filepath.Join(e.root, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		in := false
		for _, d := range []string{e.live, e.backups, e.staging, e.export} {
			if full == d || strings.HasPrefix(full, d+string(filepath.Separator)) {
				in = true
			}
		}
		// The folders above them may be made, and that is all.
		for _, d := range []string{e.live, e.backups, e.staging, e.export} {
			if strings.HasSuffix(p, "/") && strings.HasPrefix(d, full+string(filepath.Separator)) {
				in = true
			}
		}
		if !in {
			out[p] = data
		}
	}
	return out
}
