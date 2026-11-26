package worldstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// Every operation, round trip, for both layouts: list, backup, restore,
// rename, export and import, delete and bring back, import from the
// owner's own game. Nothing outside the store's folders changes.
func TestRoundTrips(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			l.make(t, e.live, "Alpha", "one")
			l.make(t, e.saves, "Delta", "mine") // the owner's own game
			before := outside(t, e)
			w := New(l.format(e), e.backups, e.staging)
			alpha := world(t, l, e.live, "Alpha")

			// List.
			list, orphans, err := w.List()
			if err != nil || len(list) != 1 || len(orphans) != 0 {
				t.Fatalf("list = %+v %+v %v", list, orphans, err)
			}
			if a := list[0]; a.Name != "Alpha" || a.Format != l.kind || a.Size < 2000 || a.Modified.IsZero() || a.Backups == nil || a.NeverSaved {
				t.Fatalf("Alpha = %+v", a)
			}
			if !w.Exists("Alpha") || w.Exists("Beta") {
				t.Fatal("Exists is wrong")
			}
			if _, err := w.Get("Beta"); !errors.Is(err, worlds.ErrNotFound) {
				t.Fatalf("Get(Beta) = %v", err)
			}

			// Backup: a zip laid out as the world is in its folder.
			b, err := w.Backup("Alpha", "manual")
			if err != nil {
				t.Fatal(err)
			}
			if b.Kind != worlds.KindApp || b.Label != "Backup made in this app" || b.Size == 0 || b.Format != l.kind {
				t.Fatalf("backup = %+v", b)
			}
			zipPath := filepath.Join(e.backups, "Alpha", strings.TrimPrefix(b.ID, "app:"))
			if got := zipNames(t, zipPath); !reflect.DeepEqual(got, l.paths("Alpha")) {
				t.Fatalf("zip holds %v, want %v", got, l.paths("Alpha"))
			}
			var buf bytes.Buffer
			if err := w.ExportBackup(&buf, "Alpha", b.ID); err != nil {
				t.Fatal(err)
			}
			if onDisk, _ := os.ReadFile(zipPath); !bytes.Equal(buf.Bytes(), onDisk) {
				t.Fatal("ExportBackup isn't the backup's zip")
			}

			// Restore: the world changes, then comes back.
			os.RemoveAll(filepath.Join(e.live, "Alpha"))
			l.make(t, e.live, "Alpha", "two")
			two := world(t, l, e.live, "Alpha")
			if sameMap(two, alpha) {
				t.Fatal("the world didn't change")
			}
			if err := w.Restore("Alpha", b.ID); err != nil {
				t.Fatal(err)
			}
			if got := world(t, l, e.live, "Alpha"); !sameMap(got, alpha) {
				t.Fatalf("restored %v, want %v", keys(got), keys(alpha))
			}
			if got := backupsOf(t, w, "Alpha"); len(got) != 2 || !contains(got, "before-restore") {
				t.Fatalf("backups = %v", got)
			}
			noHidden(t, e.live)

			// Rename, with the backups.
			if err := w.Rename("Alpha", "Beta"); err != nil {
				t.Fatal(err)
			}
			if w.Exists("Alpha") || !sameMap(world(t, l, e.live, "Beta"), alpha) {
				t.Fatalf("after rename: %v", keys(tree(t, e.live)))
			}
			if got := backupsOf(t, w, "Beta"); len(got) != 3 || !contains(got, "before-rename") {
				t.Fatalf("Beta's backups = %v", got)
			}
			// A backup made under the old name comes back under the new one.
			if err := w.Restore("Beta", findBackup(t, w, "Beta", "before-restore").ID); err != nil {
				t.Fatal(err)
			}
			if w.Exists("Alpha") || !sameMap(world(t, l, e.live, "Beta"), two) {
				t.Fatalf("after restoring an old-name backup: %v", keys(tree(t, e.live)))
			}
			if err := w.Restore("Beta", findBackup(t, w, "Beta", "manual").ID); err != nil {
				t.Fatal(err)
			}

			// Export, then import the download as a second world.
			zipped, err := w.ExportFile("Beta", e.export)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(zipped) != e.export {
				t.Fatalf("export written to %s", zipped)
			}
			upload := filepath.Join(e.staging, "upload-1")
			data, _ := os.ReadFile(zipped)
			writeFile(t, filepath.Join(upload, "Beta 2026-11-26.zip"), string(data))
			cands := w.Scan(upload)
			if len(cands) != 1 || cands[0].Name != "Beta" || !cands[0].Exists || cands[0].FreeName != "Beta 2" || cands[0].Problem != "" || cands[0].ServerSize == 0 {
				t.Fatalf("candidates = %+v", cands)
			}
			done, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "import", Name: "Gamma"}}, never, true)
			if err != nil || len(done) != 1 || done[0].Name != "Gamma" || done[0].Replaced {
				t.Fatalf("import = %+v, %v", done, err)
			}
			if !sameMap(world(t, l, e.live, "Gamma"), alpha) {
				t.Fatalf("Gamma = %v", keys(world(t, l, e.live, "Gamma")))
			}

			// Delete, then bring it back from the copy kept.
			if err := w.Delete("Gamma"); err != nil {
				t.Fatal(err)
			}
			if w.Exists("Gamma") {
				t.Fatal("Gamma is still there")
			}
			_, orphans, _ = w.List()
			if len(orphans) != 1 || orphans[0].Name != "Gamma" || len(orphans[0].Backups) != 1 {
				t.Fatalf("orphans = %+v", orphans)
			}
			if err := w.Restore("Gamma", orphans[0].Backups[0].ID); err != nil {
				t.Fatal(err)
			}
			if !sameMap(world(t, l, e.live, "Gamma"), alpha) {
				t.Fatal("Gamma didn't come back as it was")
			}

			// The owner's own game: copied, never moved.
			mine := tree(t, e.saves)
			local := w.Local()
			if len(local) != 1 || local[0].Name != "Delta" || local[0].Source != e.saves || local[0].Exists {
				t.Fatalf("local = %+v", local)
			}
			if _, err := w.Import(local, importAll(local, "import"), never, false); err != nil {
				t.Fatal(err)
			}
			if !sameMap(world(t, l, e.live, "Delta"), world(t, l, e.saves, "Delta")) {
				t.Fatal("Delta wasn't copied as it is")
			}
			if !sameMap(tree(t, e.saves), mine) {
				t.Fatal("the owner's own saves changed")
			}

			names := []string{}
			list, _, _ = w.List()
			for _, x := range list {
				names = append(names, x.Name)
			}
			if !reflect.DeepEqual(names, []string{"Beta", "Delta", "Gamma"}) {
				t.Fatalf("worlds = %v", names)
			}
			noHidden(t, e.live)
			if after := outside(t, e); !sameMap(after, before) {
				t.Fatalf("files outside the store's folders changed: before %v, after %v", keys(before), keys(after))
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Replacing a world: refused without Replace and while it runs; with
// Replace, the server's copy is backed up first.
func TestImportReplace(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			l.make(t, e.live, "Alpha", "server")
			upload := filepath.Join(e.staging, "upload-1")
			l.make(t, upload, "Alpha", "friend")
			friend := world(t, l, upload, "Alpha")
			cands := w.Scan(upload)
			if len(cands) != 1 || !cands[0].Exists || cands[0].FreeName != "Alpha 2" {
				t.Fatalf("candidates = %+v", cands)
			}
			id := cands[0].ID
			if _, err := w.Import(cands, []worlds.Choice{{ID: id, Action: "import"}}, never, true); err == nil || !strings.Contains(err.Error(), "Choose Replace") {
				t.Fatalf("err = %v", err)
			}
			running := func(name string) bool { return name == "Alpha" }
			if _, err := w.Import(cands, []worlds.Choice{{ID: id, Action: "replace"}}, running, true); err == nil || !strings.Contains(err.Error(), "running on the server") {
				t.Fatalf("err = %v", err)
			}
			if _, err := w.Import(cands, []worlds.Choice{{ID: "u999", Action: "import"}}, never, true); err == nil || !strings.Contains(err.Error(), "expired") {
				t.Fatalf("err = %v", err)
			}
			for _, bad := range []string{"", " Alpha", "a/b", "CON", ".hidden", "-x", strings.Repeat("x", 41)} {
				if _, err := w.Import(cands, []worlds.Choice{{ID: id, Name: bad, Action: "import"}}, never, true); err == nil {
					t.Fatalf("name %q was taken", bad)
				}
			}
			if got := backupsOf(t, w, "Alpha"); len(got) != 0 {
				t.Fatalf("refused imports made backups: %v", got)
			}
			done, err := w.Import(cands, []worlds.Choice{{ID: id, Action: "replace"}}, never, true)
			if err != nil || len(done) != 1 || !done[0].Replaced || done[0].Was != "" {
				t.Fatalf("import = %+v, %v", done, err)
			}
			if !sameMap(world(t, l, e.live, "Alpha"), friend) {
				t.Fatal("Alpha isn't the friend's copy")
			}
			b := findBackup(t, w, "Alpha", "before-replace")
			if b.Label != "Automatic copy before it was replaced" {
				t.Fatalf("backup = %+v", b)
			}
			noHidden(t, e.live)
		})
	}
}

// Files left under a name by a world that is gone (a lone backup file) are
// kept in a backup and cleared, so a new world of that name is whole.
func TestImportOverLeftovers(t *testing.T) {
	e := newEnv(t)
	l := layouts[1]
	w := New(l.format(e), e.backups, e.staging)
	writeFile(t, filepath.Join(e.live, "Alpha.wld.bak"), "left over")
	if w.Exists("Alpha") {
		t.Fatal("a lone backup file counts as a world")
	}
	upload := filepath.Join(e.staging, "upload-1")
	writeFile(t, filepath.Join(upload, "Alpha.wld"), "new world")
	cands := w.Scan(upload)
	if len(cands) != 1 || cands[0].Exists {
		t.Fatalf("candidates = %+v", cands)
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, e.live); !sameMap(got, map[string]string{"Alpha.wld": "new world"}) {
		t.Fatalf("live = %v", got)
	}
	b := findBackup(t, w, "Alpha", "before-replace")
	if names := zipNames(t, filepath.Join(e.backups, "Alpha", strings.TrimPrefix(b.ID, "app:"))); !reflect.DeepEqual(names, []string{"Alpha.wld.bak"}) {
		t.Fatalf("backup holds %v", names)
	}
}

// Two copies of one world in an upload (two zips of it) stay separate:
// their files never mix, the newer one is offered, and the older one can
// still be chosen under another name.
func TestTwoZipsOfOneWorld(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			upload := filepath.Join(e.staging, "upload-1")
			older, newer := t.TempDir(), t.TempDir()
			l.make(t, older, "Alpha", "old")
			l.make(t, newer, "Alpha", "new")
			files := func(dir string) map[string]string {
				out := map[string]string{}
				for p, data := range tree(t, dir) {
					if !strings.HasSuffix(p, "/") {
						out[p] = data
					}
				}
				return out
			}
			// The newer one comes first, so it would lose if both were
			// unpacked into one folder.
			buildZipAt(t, filepath.Join(upload, "a.zip"), mustTime(t, "2025-01-01T12:00:00Z"), files(newer))
			buildZipAt(t, filepath.Join(upload, "b.zip"), mustTime(t, "2024-01-01T12:00:00Z"), files(older))
			cands := w.Scan(upload)
			if len(cands) != 2 || cands[0].ID == cands[1].ID {
				t.Fatalf("candidates = %+v", cands)
			}
			var oldID string
			for _, c := range cands {
				if c.Name != "Alpha" || c.Exists {
					t.Fatalf("candidate = %+v", c)
				}
				if c.Superseded {
					oldID = c.ID
				}
			}
			if oldID == "" || cands[0].Superseded {
				t.Fatalf("the newer copy isn't the one offered: %+v", cands)
			}
			if _, err := w.Import(cands, importAll(cands, "import"), never, true); err != nil {
				t.Fatal(err)
			}
			// The zips hold files only (no empty folders), so compare files.
			if !sameMap(worldFiles(t, l, e.live, "Alpha"), worldFiles(t, l, newer, "Alpha")) {
				t.Fatalf("Alpha = %v", world(t, l, e.live, "Alpha"))
			}
			if _, err := w.Import(cands, []worlds.Choice{{ID: oldID, Action: "import", Name: "Older"}}, never, true); err != nil {
				t.Fatal(err)
			}
			if !sameMap(worldFiles(t, l, e.live, "Older"), worldFiles(t, l, older, "Alpha")) {
				t.Fatalf("Older = %v", world(t, l, e.live, "Older"))
			}
		})
	}
}

// readAny reads a candidate's files, to tell copies apart.
func readAny(t *testing.T, loc located) string {
	var b strings.Builder
	if loc.dir != "" {
		for _, data := range tree(t, loc.dir) {
			b.WriteString(data)
		}
	}
	for _, f := range loc.files {
		data, _ := os.ReadFile(f.path)
		b.Write(data)
	}
	return b.String()
}

// The upload's zips and its loose files are each their own copy; a newer
// copy supersedes an older one of the same name.
func TestSupersededByModified(t *testing.T) {
	e := newEnv(t)
	l := layouts[1]
	w := New(l.format(e), e.backups, e.staging)
	upload := filepath.Join(e.staging, "upload-1")
	l.make(t, filepath.Join(upload, "old"), "Alpha", "old")
	l.make(t, filepath.Join(upload, "new"), "Alpha", "new")
	past := mustTime(t, "2024-01-01T00:00:00Z")
	for _, ext := range []string{".wld", ".wld.bak"} {
		os.Chtimes(filepath.Join(upload, "old", "Alpha"+ext), past, past)
	}
	cands := w.Scan(upload)
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v", cands)
	}
	for _, c := range cands {
		loc, _ := w.(*store).lookup(c.ID)
		isOld := strings.Contains(readAny(t, loc), "old")
		if c.Superseded != isOld {
			t.Fatalf("candidate %+v (old: %v)", c, isOld)
		}
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(world(t, l, e.live, "Alpha")["W.wld"], "new") {
		t.Fatal("the older copy was imported")
	}
}

// Zips with unsafe paths are refused: Scan says so, and nothing is
// written anywhere for them. A tampered backup is refused the same way.
func TestUnsafeZipPathsRefused(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			upload := filepath.Join(e.staging, "upload-1")
			bad := []string{"../evil.wld", "../../evil.wld", "/abs/evil.wld", `..\evil.wld`, "C:/evil.wld", "Alpha/../../evil.wld", "a/b/../../../evil.wld"}
			for i, name := range bad {
				buildZip(t, filepath.Join(upload, "bad"+string(rune('a'+i))+".zip"), map[string]string{
					"Alpha/level.dat": "fine", "Alpha.wld": "fine", name: "escaped",
				})
			}
			before := outside(t, e)
			cands := w.Scan(upload)
			if len(cands) != len(bad) {
				t.Fatalf("candidates = %+v", cands)
			}
			for _, c := range cands {
				if !strings.Contains(c.Problem, "unsafe path") {
					t.Fatalf("candidate = %+v", c)
				}
				if _, err := w.Import(cands, []worlds.Choice{{ID: c.ID, Action: "import"}}, never, true); err == nil || !strings.Contains(err.Error(), "unsafe path") {
					t.Fatalf("import of %+v: %v", c, err)
				}
			}
			if after := outside(t, e); !sameMap(after, before) {
				t.Fatalf("an unsafe zip wrote outside: %v", keys(after))
			}
			// Nothing was unpacked from them either.
			entries, _ := os.ReadDir(e.staging)
			for _, en := range entries {
				if strings.HasPrefix(en.Name(), "unzipped-") {
					t.Fatalf("%s was left in staging", en.Name())
				}
			}
			for p := range tree(t, e.root) {
				if strings.Contains(p, "evil") {
					t.Fatalf("%s was written", p)
				}
			}

			// A backup replaced by an unsafe zip changes nothing.
			l.make(t, e.live, "Alpha", "server")
			alpha := world(t, l, e.live, "Alpha")
			b, err := w.Backup("Alpha", "manual")
			if err != nil {
				t.Fatal(err)
			}
			buildZip(t, filepath.Join(e.backups, "Alpha", strings.TrimPrefix(b.ID, "app:")), map[string]string{"../../evil.wld": "x"})
			if err := w.Restore("Alpha", b.ID); err == nil || !strings.Contains(err.Error(), "damaged") {
				t.Fatalf("err = %v", err)
			}
			if !sameMap(world(t, l, e.live, "Alpha"), alpha) {
				t.Fatal("the world changed")
			}
			for p := range tree(t, e.root) {
				if strings.Contains(p, "evil") {
					t.Fatalf("%s was written", p)
				}
			}
			noHidden(t, e.live)
			// Backup IDs and names can't reach other folders either.
			for _, id := range []string{"app:../../x.zip", `app:..\x.zip`, "app:", "x.zip", "app:" + strings.TrimPrefix(b.ID, "app:") + "/.."} {
				if err := w.Restore("Alpha", id); err == nil {
					t.Fatalf("restore of %q worked", id)
				}
				if err := w.ExportBackup(&bytes.Buffer{}, "Alpha", id); err == nil {
					t.Fatalf("export of %q worked", id)
				}
			}
			if err := w.ExportBackup(&bytes.Buffer{}, "../Alpha", b.ID); err == nil {
				t.Fatal("a name with a path reached another folder")
			}
		})
	}
}

// With Go set to refuse unsafe zips on its own, the store still opens
// them to say which path is unsafe, and still writes nothing.
func TestUnsafeZipWithStrictGo(t *testing.T) {
	t.Setenv("GODEBUG", "zipinsecurepath=0")
	e := newEnv(t)
	w := New(layouts[0].format(e), e.backups, e.staging)
	upload := filepath.Join(e.staging, "upload-1")
	buildZip(t, filepath.Join(upload, "bad.zip"), map[string]string{"Alpha/level.dat": "x", "../evil.dat": "x"})
	cands := w.Scan(upload)
	if len(cands) != 1 || !strings.Contains(cands[0].Problem, "unsafe path (../evil.dat)") {
		t.Fatalf("candidates = %+v", cands)
	}
	for p := range tree(t, e.root) {
		if strings.Contains(p, "evil") || strings.Contains(p, "level.dat") {
			t.Fatalf("%s was written", p)
		}
	}
}

// lockedRename makes a move of the named file or folder fail, as Windows
// does for a file another program holds open.
func lockedRename(name string) func(string, string) error {
	return func(from, to string) error {
		if filepath.Base(from) == name {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("the file is being used by another process")}
		}
		return os.Rename(from, to)
	}
}

// If a world's files can't all be moved out of the way, a restore,
// replace, rename or delete changes nothing, instead of leaving the world
// half gone. (The real Windows lock is in locked_windows_test.go.)
func TestLockedFileLeavesWorldAlone(t *testing.T) {
	t.Cleanup(func() { renameFile = os.Rename })
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			l.make(t, e.live, "Alpha", "one")
			b, err := w.Backup("Alpha", "manual")
			if err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(filepath.Join(e.live, "Alpha"))
			l.make(t, e.live, "Alpha", "two")
			now := world(t, l, e.live, "Alpha")
			upload := filepath.Join(e.staging, "upload-1")
			l.make(t, upload, "Alpha", "friend")
			cands := w.Scan(upload)

			// The folder, or the second of the world's files, is held.
			locked := "Alpha"
			if l.kind == KindFiles {
				locked = "Alpha.wld.bak"
			}
			renameFile = lockedRename(locked)
			check := func(what string, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
					t.Fatalf("%s: err = %v", what, err)
				}
				if !sameMap(world(t, l, e.live, "Alpha"), now) {
					t.Fatalf("%s changed the world: %v", what, keys(tree(t, e.live)))
				}
				if got := backupsOf(t, w, "Alpha"); !reflect.DeepEqual(got, []string{"manual"}) {
					t.Fatalf("%s left backups %v", what, got)
				}
				noHidden(t, e.live)
			}
			check("restore", w.Restore("Alpha", b.ID))
			check("delete", w.Delete("Alpha"))
			check("rename", w.Rename("Alpha", "Beta"))
			_, err = w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "replace"}}, never, true)
			check("replace", err)
			if w.Exists("Beta") {
				t.Fatal("Beta exists")
			}

			// Once the file is free, the same changes work.
			renameFile = os.Rename
			if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "replace"}}, never, true); err != nil {
				t.Fatal(err)
			}
			if err := w.Restore("Alpha", b.ID); err != nil {
				t.Fatal(err)
			}
			noHidden(t, e.live)
		})
	}
}

// If a file can't be moved out of the way and one already moved can't be
// put back either, the world is only partly there and the next change
// clears the rest. The change must then keep its safety backup and say to
// restore from it, instead of claiming nothing changed and deleting the
// only whole copy.
func TestUndoFailureKeepsSafetyBackup(t *testing.T) {
	t.Cleanup(func() { renameFile = os.Rename })
	l := layouts[1] // a "files" world has two files, so one can move and the next fail
	stuck := func(from, to string) error {
		// Alpha.wld.bak is held; Alpha.wld moves aside but can't come back.
		if filepath.Base(from) == "Alpha.wld.bak" || (strings.Contains(from, removingPrefix) && filepath.Base(from) == "Alpha.wld") {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("the file is being used by another process")}
		}
		return os.Rename(from, to)
	}
	changes := []struct {
		what, reason string
		run          func(w game.Worlds, manual string, cands []worlds.Candidate) error
	}{
		{"restore", "before-restore", func(w game.Worlds, manual string, _ []worlds.Candidate) error {
			return w.Restore("Alpha", manual)
		}},
		{"delete", "deleted", func(w game.Worlds, _ string, _ []worlds.Candidate) error {
			return w.Delete("Alpha")
		}},
		{"rename", "before-rename", func(w game.Worlds, _ string, _ []worlds.Candidate) error {
			return w.Rename("Alpha", "Beta")
		}},
		{"replace", "before-replace", func(w game.Worlds, _ string, cands []worlds.Candidate) error {
			_, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "replace"}}, never, true)
			return err
		}},
	}
	for _, c := range changes {
		t.Run(c.what, func(t *testing.T) {
			renameFile = os.Rename
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			l.make(t, e.live, "Alpha", "one")
			manual, err := w.Backup("Alpha", "manual")
			if err != nil {
				t.Fatal(err)
			}
			l.make(t, e.live, "Alpha", "two")
			l.make(t, e.live, "Gamma", "other")
			now := world(t, l, e.live, "Alpha")
			upload := filepath.Join(e.staging, "upload-1")
			l.make(t, upload, "Alpha", "friend")
			cands := w.Scan(upload)

			renameFile = stuck
			err = c.run(w, manual.ID, cands)
			safety := findBackup(t, w, "Alpha", c.reason)
			if err == nil || strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "only partly there") || !strings.Contains(err.Error(), safety.Label) {
				t.Fatalf("err = %v", err)
			}

			// The next change clears the hidden folder that holds the
			// rest of Alpha; its safety backup still brings it back whole.
			renameFile = os.Rename
			if err := w.Delete("Gamma"); err != nil {
				t.Fatal(err)
			}
			if err := w.Restore("Alpha", safety.ID); err != nil {
				t.Fatal(err)
			}
			if !sameMap(world(t, l, e.live, "Alpha"), now) {
				t.Fatalf("world = %v", world(t, l, e.live, "Alpha"))
			}
			noHidden(t, e.live)
		})
	}
}

// A rename whose second file can't take the new name, and whose first file
// can't go back to the hidden folder, leaves that first file under the new
// name rather than deleting it, and keeps the safety backup.
func TestRenameUndoFailureKeepsFiles(t *testing.T) {
	t.Cleanup(func() { renameFile = os.Rename })
	l := layouts[1]
	e := newEnv(t)
	w := New(l.format(e), e.backups, e.staging)
	l.make(t, e.live, "Alpha", "one")
	want := world(t, l, e.live, "Alpha")
	renameFile = func(from, to string) error {
		if filepath.Base(to) == "Beta.wld.bak" || filepath.Base(from) == "Beta.wld" {
			return errors.New("disk full")
		}
		return os.Rename(from, to)
	}
	err := w.Rename("Alpha", "Beta")
	renameFile = os.Rename
	safety := findBackup(t, w, "Alpha", "before-rename")
	if err == nil || !strings.Contains(err.Error(), safety.Label) {
		t.Fatalf("err = %v", err)
	}
	got, rerr := os.ReadFile(filepath.Join(e.live, "Beta.wld"))
	if rerr != nil || string(got) != want["W.wld"] {
		t.Fatalf("Beta.wld = %q, %v (want Alpha's first file kept)", got, rerr)
	}
}

// A move that fails halfway while putting the new copy in place puts the
// old world back.
func TestFailedPlacePutsWorldBack(t *testing.T) {
	t.Cleanup(func() { renameFile = os.Rename })
	l := layouts[1]
	e := newEnv(t)
	w := New(l.format(e), e.backups, e.staging)
	l.make(t, e.live, "Alpha", "one")
	b, _ := w.Backup("Alpha", "manual")
	os.Remove(filepath.Join(e.live, "Alpha.wld"))
	l.make(t, e.live, "Alpha", "two")
	now := world(t, l, e.live, "Alpha")
	// Moving the old copy aside works; putting the backup's second file in
	// place doesn't.
	calls := 0
	renameFile = func(from, to string) error {
		if strings.Contains(from, restorePrefix) && filepath.Base(from) == "Alpha.wld.bak" {
			calls++
			return errors.New("disk full")
		}
		return os.Rename(from, to)
	}
	if err := w.Restore("Alpha", b.ID); err == nil || !strings.Contains(err.Error(), "left as it was") || calls != 1 {
		t.Fatalf("err = %v (%d calls)", err, calls)
	}
	if !sameMap(world(t, l, e.live, "Alpha"), now) {
		t.Fatalf("world = %v", world(t, l, e.live, "Alpha"))
	}
	noHidden(t, e.live)
}

// A backup of the running world works when the server saves while it is
// being zipped: the copy starts again with the new files. A server that
// never stops saving gets a clear message, and no half copy is kept.
func TestBackupWhileSaving(t *testing.T) {
	old := retryDelay
	retryDelay = 0
	t.Cleanup(func() { retryDelay, zipping = old, nil })
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			l.make(t, e.live, "Alpha", "one")
			saves := 0
			zipping = func() {
				saves++
				if saves == 1 {
					l.make(t, e.live, "Alpha", "saved-while-zipping")
				}
			}
			b, err := w.Backup("Alpha", "manual")
			if err != nil || saves != 2 {
				t.Fatalf("backup after %d tries: %v", saves, err)
			}
			want := world(t, l, e.live, "Alpha")
			os.RemoveAll(filepath.Join(e.live, "Alpha"))
			l.make(t, e.live, "Alpha", "later")
			if err := w.Restore("Alpha", b.ID); err != nil {
				t.Fatal(err)
			}
			if !sameMap(world(t, l, e.live, "Alpha"), want) {
				t.Fatal("the backup isn't the saved world")
			}

			zipping = func() {
				saves++
				writeFile(t, filepath.Join(e.live, l.paths("Alpha")[len(l.paths("Alpha"))-1]), strings.Repeat("s", saves))
			}
			if _, err := w.Backup("Alpha", "manual"); err == nil || !strings.Contains(err.Error(), "kept saving") {
				t.Fatalf("err = %v", err)
			}
			if _, err := w.ExportFile("Alpha", e.export); err == nil || !strings.Contains(err.Error(), "kept saving") {
				t.Fatalf("err = %v", err)
			}
			zipping = nil
			files, _ := os.ReadDir(filepath.Join(e.backups, "Alpha"))
			for _, f := range files {
				if !strings.HasSuffix(f.Name(), ".zip") {
					t.Fatalf("%s left in the backups", f.Name())
				}
			}
			if exports, _ := os.ReadDir(e.export); len(exports) != 0 {
				t.Fatalf("exports left: %v", exports)
			}
		})
	}
}

// A game that lays its worlds out itself (Format.List and Format.Files):
// backups hold exactly its paths, restores and deletes touch nothing else,
// and paths outside Dir are refused.
func TestCustomLayout(t *testing.T) {
	e := newEnv(t)
	paths := func(name string) []string {
		return []string{filepath.Join("Saves", "Multiplayer", name), filepath.Join("db", name+".db"), filepath.Join(e.live, "Server", name+".ini")}
	}
	f := Format{
		Dir: e.live, Kind: KindFolder,
		List: func() ([]Live, error) {
			entries, _ := os.ReadDir(filepath.Join(e.live, "Saves", "Multiplayer"))
			var out []Live
			for _, en := range entries {
				out = append(out, Live{Name: en.Name()})
			}
			return out, nil
		},
		Files: func(name string) ([]string, error) { return paths(name), nil },
	}
	w := New(f, e.backups, e.staging)
	for _, n := range []string{"Alpha", "Other"} {
		writeFile(t, filepath.Join(e.live, "Saves", "Multiplayer", n, "map_1_1.bin"), "map "+n)
		writeFile(t, filepath.Join(e.live, "db", n+".db"), "db "+n)
		writeFile(t, filepath.Join(e.live, "Server", n+".ini"), "ini "+n)
	}
	writeFile(t, filepath.Join(e.live, "Server", "shared.txt"), "not a world's")
	all := tree(t, e.live)

	b, err := w.Backup("Alpha", "manual")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Saves/Multiplayer/Alpha/", "Saves/Multiplayer/Alpha/map_1_1.bin", "Server/Alpha.ini", "db/Alpha.db"}
	if got := zipNames(t, filepath.Join(e.backups, "Alpha", strings.TrimPrefix(b.ID, "app:"))); !reflect.DeepEqual(got, want) {
		t.Fatalf("zip = %v", got)
	}
	if err := w.Delete("Alpha"); err != nil {
		t.Fatal(err)
	}
	for p := range all {
		_, still := tree(t, e.live)[p]
		if strings.Contains(p, "Alpha") == still && !strings.HasSuffix(p, "/") {
			t.Fatalf("%s: still there = %v", p, still)
		}
	}
	if err := w.Restore("Alpha", b.ID); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, e.live); !sameMap(got, all) {
		t.Fatalf("after restore %v, want %v", keys(got), keys(all))
	}
	if err := w.Rename("Alpha", "Beta"); !errors.Is(err, game.ErrCantRename) {
		t.Fatalf("rename = %v", err)
	}
	noHidden(t, e.live)

	// A path outside Dir is refused, and nothing is read or moved there.
	writeFile(t, filepath.Join(e.root, "elsewhere", "keep.txt"), "mine")
	f.Files = func(name string) ([]string, error) {
		return []string{filepath.Join("db", name+".db"), filepath.Join("..", "..", "..", "elsewhere")}, nil
	}
	w = New(f, e.backups, e.staging)
	if _, err := w.Backup("Alpha", "manual"); err == nil || !strings.Contains(err.Error(), "inside its worlds folder") {
		t.Fatalf("err = %v", err)
	}
	if err := w.Delete("Alpha"); err == nil {
		t.Fatal("delete worked")
	}
	if got := tree(t, filepath.Join(e.root, "elsewhere")); got["keep.txt"] != "mine" {
		t.Fatal("the folder outside was touched")
	}
	if !sameMap(tree(t, e.live), all) {
		t.Fatal("the worlds changed")
	}
}

// Format.Place runs after the copy; if it fails, the import is undone.
func TestPlaceHook(t *testing.T) {
	e := newEnv(t)
	l := layouts[0]
	f := l.format(e)
	var placed []string
	f.Place = func(c worlds.Candidate, name string) error {
		placed = append(placed, name)
		if name == "Broken" {
			// Something was changed in the copy before it failed.
			writeFile(t, filepath.Join(e.live, name, "level.dat"), "half renamed")
			return errors.New("couldn't write level.dat")
		}
		return os.WriteFile(filepath.Join(e.live, name, "level.dat"), []byte("name="+name), 0o644)
	}
	w := New(f, e.backups, e.staging)
	l.make(t, e.live, "Broken", "server")
	server := world(t, l, e.live, "Broken")
	upload := filepath.Join(e.staging, "upload-1")
	l.make(t, upload, "Friend", "friend")
	cands := w.Scan(upload)
	if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "replace", Name: "Broken"}}, never, true); err == nil || !strings.Contains(err.Error(), "left as it was") {
		t.Fatalf("err = %v", err)
	}
	if !sameMap(world(t, l, e.live, "Broken"), server) {
		t.Fatal("the server's copy changed")
	}
	if got := backupsOf(t, w, "Broken"); len(got) != 0 {
		t.Fatalf("backups = %v", got)
	}
	// The upload got its files back, so it can be imported again.
	if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "import", Name: "Good"}}, never, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(e.live, "Good", "level.dat")); string(data) != "name=Good" {
		t.Fatalf("level.dat = %q", data)
	}
	if !reflect.DeepEqual(placed, []string{"Broken", "Good"}) {
		t.Fatalf("placed = %v", placed)
	}
	noHidden(t, e.live)
}

// A game whose worlds are zips (Necesse): an uploaded world zip is offered
// as it is, and a zip of world zips is unpacked.
func TestWorldsThatAreZips(t *testing.T) {
	e := newEnv(t)
	w := New(Format{Dir: e.live, Kind: KindFiles, Ext: []string{".zip"}}, e.backups, e.staging)
	island := filepath.Join(t.TempDir(), "Island.zip")
	buildZip(t, island, map[string]string{"world.dat": "island", "settings.cfg": "x"})
	islandData, _ := os.ReadFile(island)
	upload := filepath.Join(e.staging, "upload-1")
	writeFile(t, filepath.Join(upload, "Island.zip"), string(islandData))
	buildZip(t, filepath.Join(upload, "my worlds.zip"), map[string]string{"saves/Cave.zip": string(islandData), "saves/Sea.zip": string(islandData)})
	cands := w.Scan(upload)
	var names []string
	for _, c := range cands {
		names = append(names, c.Name)
		if c.Problem != "" {
			t.Fatalf("candidate = %+v", c)
		}
	}
	if !reflect.DeepEqual(names, []string{"Island", "Cave", "Sea"}) {
		t.Fatalf("names = %v", names)
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if data, _ := os.ReadFile(filepath.Join(e.live, n+".zip")); !bytes.Equal(data, islandData) {
			t.Fatalf("%s.zip isn't the world", n)
		}
	}
}

// A game's own Detect (Minecraft: a folder holding level.dat) finds the
// worlds; an ID pointing outside the upload is ignored.
func TestDetectFunc(t *testing.T) {
	e := newEnv(t)
	f := layouts[0].format(e)
	f.Detect = func(root string) []worlds.Candidate {
		var out []worlds.Candidate
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.Name() == "level.dat" {
				rel, _ := filepath.Rel(root, filepath.Dir(p))
				out = append(out, worlds.Candidate{ID: filepath.ToSlash(rel), Renamable: true})
			}
			return nil
		})
		return append(out, worlds.Candidate{ID: "../../..", Name: "Escape", Renamable: true},
			worlds.Candidate{ID: "nowhere", Name: "Old", Problem: "This world is from a newer version."})
	}
	w := New(f, e.backups, e.staging)
	upload := filepath.Join(e.staging, "upload-1")
	writeFile(t, filepath.Join(upload, "deep", "deeper", "Mine", "level.dat"), "m")
	writeFile(t, filepath.Join(upload, "readme.txt"), "not a world")
	cands := w.Scan(upload)
	if len(cands) != 2 || cands[0].Name != "Mine" || cands[1].Problem == "" {
		t.Fatalf("candidates = %+v", cands)
	}
	if _, err := w.Import(cands, []worlds.Choice{{ID: cands[1].ID, Action: "import"}}, never, true); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("err = %v", err)
	}
	if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "import"}}, never, false); err != nil {
		t.Fatal(err)
	}
	if !w.Exists("Mine") {
		t.Fatal("Mine wasn't imported")
	}
}

// Loose world files without a folder (a zip of a world folder's contents)
// are named after the zip; loose files in the upload itself need a name.
func TestLooseFiles(t *testing.T) {
	e := newEnv(t)
	w := New(layouts[0].format(e), e.backups, e.staging)
	upload := filepath.Join(e.staging, "upload-1")
	buildZip(t, filepath.Join(upload, "Island.zip"), map[string]string{"level.dat": "x", "region/r.0.0.mca": "r"})
	cands := w.Scan(upload)
	if len(cands) != 1 || cands[0].Name != "Island" || !cands[0].Renamable {
		t.Fatalf("candidates = %+v", cands)
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, filepath.Join(e.live, "Island")); got["level.dat"] != "x" || got["region/r.0.0.mca"] != "r" {
		t.Fatalf("Island = %v", got)
	}

	other := filepath.Join(e.staging, "upload-2")
	writeFile(t, filepath.Join(other, "level.dat"), "y")
	cands = w.Scan(other)
	if len(cands) != 1 || cands[0].Name != "" || !cands[0].Renamable {
		t.Fatalf("candidates = %+v", cands)
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err == nil {
		t.Fatal("a world without a name was imported")
	}
	if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "import", Name: "Named"}}, never, true); err != nil {
		t.Fatal(err)
	}
	// The upload's own folder is copied, never moved away.
	if _, err := os.Stat(filepath.Join(other, "level.dat")); err != nil {
		t.Fatal("the upload's own folder moved")
	}
}

// The store's own hidden folders, and the app's folders if a game's Dir
// holds them, are never listed as worlds.
func TestListSkipsOwnFolders(t *testing.T) {
	e := newEnv(t)
	f := layouts[0].format(e)
	w := New(f, filepath.Join(e.live, "backups"), filepath.Join(e.live, "uploads"))
	layouts[0].make(t, e.live, "Alpha", "x")
	writeFile(t, filepath.Join(e.live, ".egs-removing-123", "Old", "level.dat"), "old")
	writeFile(t, filepath.Join(e.live, "uploads", "u", "level.dat"), "u")
	if _, err := w.Backup("Alpha", "manual"); err != nil {
		t.Fatal(err)
	}
	list, _, err := w.List()
	if err != nil || len(list) != 1 || list[0].Name != "Alpha" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// The next change clears what an earlier one left behind.
	if err := w.Rename("Alpha", "Beta"); err != nil {
		t.Fatal(err)
	}
	noHidden(t, e.live)
}

// A world can be renamed to its own name in other letters; a taken name or
// a bad one is refused.
func TestRenameRules(t *testing.T) {
	for _, l := range layouts {
		t.Run(l.kind, func(t *testing.T) {
			e := newEnv(t)
			w := New(l.format(e), e.backups, e.staging)
			l.make(t, e.live, "Alpha", "a")
			l.make(t, e.live, "Other", "o")
			if err := w.Rename("Alpha", "Other"); err == nil || !strings.Contains(err.Error(), "already") {
				t.Fatalf("err = %v", err)
			}
			if err := w.Rename("Alpha", "bad/name"); err == nil {
				t.Fatal("a bad name was taken")
			}
			if err := w.Rename("Missing", "New"); !errors.Is(err, worlds.ErrNotFound) {
				t.Fatalf("err = %v", err)
			}
			if err := w.Rename("Alpha", "ALPHA"); err != nil {
				t.Fatal(err)
			}
			list, _, _ := w.List()
			if len(list) != 2 || list[0].Name != "ALPHA" {
				t.Fatalf("list = %+v", list)
			}
			if got := backupsOf(t, w, "ALPHA"); !reflect.DeepEqual(got, []string{"before-rename"}) {
				t.Fatalf("backups = %v", got)
			}
			if l.kind == KindFiles {
				// A file left from another world isn't overwritten.
				writeFile(t, filepath.Join(e.live, "Gone.wld.bak"), "someone's backup")
				if err := w.Rename("ALPHA", "Gone"); err == nil || !strings.Contains(err.Error(), "Gone.wld.bak") {
					t.Fatalf("err = %v", err)
				}
			}
			// Renamed onto the name of a deleted world, its backups join
			// that world's, and none are lost.
			if err := w.Delete("Other"); err != nil {
				t.Fatal(err)
			}
			if err := w.Rename("ALPHA", "Other"); err != nil {
				t.Fatal(err)
			}
			if got := backupsOf(t, w, "Other"); len(got) != 3 {
				t.Fatalf("Other's backups = %v", got)
			}
			if _, orphans, _ := w.List(); len(orphans) != 0 {
				t.Fatalf("orphans = %+v", orphans)
			}
			noHidden(t, e.live)
		})
	}
}

func TestNameProblem(t *testing.T) {
	w := New(Format{Dir: t.TempDir(), Kind: KindFolder, MaxName: 10}, "", "")
	for name, ok := range map[string]bool{
		"Forest": true, "My World": true, "Über 2": true, "a.b": true,
		"": false, " ": false, " Forest": false, "Forest ": false, "Forest.": false, ".Forest": false,
		"-Forest": false, "Fo/rest": false, `Fo\rest`: false, "Fo:rest": false, "Fo*": false, "Fo?": false,
		`Fo"`: false, "Fo<": false, "Fo>": false, "Fo|": false, "Fo\x01": false,
		"CON": false, "con.txt": false, "Lpt1": false, "COM9.x": false, "CONIN$": false,
		"12345678901": false, "1234567890": true, "\xff": false,
	} {
		if got := w.NameProblem(name) == ""; got != ok {
			t.Errorf("NameProblem(%q) = %q", name, w.NameProblem(name))
		}
	}
}

// The newest backups are kept; older ones go.
func TestBackupsArePruned(t *testing.T) {
	e := newEnv(t)
	l := layouts[1]
	w := New(l.format(e), e.backups, e.staging)
	l.make(t, e.live, "Alpha", "a")
	var last worlds.Backup
	for i := 0; i < keepBackups+3; i++ {
		b, err := w.Backup("Alpha", "manual")
		if err != nil {
			t.Fatal(err)
		}
		last = b
	}
	got, _ := w.Get("Alpha")
	if len(got.Backups) != keepBackups || got.Backups[0].ID != last.ID {
		t.Fatalf("%d backups, newest %s (want %s)", len(got.Backups), got.Backups[0].ID, last.ID)
	}
	if b, _ := w.Backup("Alpha", "Odd_reason/../x"); !strings.HasSuffix(b.ID, "_oddreasonx.zip") {
		t.Fatalf("backup = %+v", b)
	}
}

// Uploads and their unpacked zips go after a while.
func TestCleanStaging(t *testing.T) {
	e := newEnv(t)
	w := New(layouts[0].format(e), e.backups, e.staging)
	upload := filepath.Join(e.staging, "upload-1")
	buildZip(t, filepath.Join(upload, "w.zip"), map[string]string{"Alpha/level.dat": "x"})
	cands := w.Scan(upload)
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v", cands)
	}
	// Scanning again reuses what was unpacked.
	if again := w.Scan(upload); len(again) != 1 {
		t.Fatalf("candidates = %+v", again)
	}
	if entries, _ := os.ReadDir(e.staging); len(entries) != 2 {
		t.Fatalf("staging = %v", entries)
	}
	w.CleanStaging(0)
	if entries, _ := os.ReadDir(e.staging); len(entries) != 0 {
		t.Fatalf("staging = %v", entries)
	}
	if _, err := w.Import(cands, importAll(cands, "import"), never, true); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}

// A store without a usable format fails clearly instead of touching files.
func TestBadFormat(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []Format{{Kind: KindFolder}, {Dir: dir, Kind: "zip"}, {Dir: dir, Kind: KindFiles}} {
		w := New(f, filepath.Join(dir, "b"), filepath.Join(dir, "s"))
		if _, _, err := w.List(); err == nil {
			t.Fatalf("%+v: List worked", f)
		}
		if w.Scan(dir) != nil || w.Local() != nil {
			t.Fatalf("%+v: found worlds", f)
		}
	}
}
