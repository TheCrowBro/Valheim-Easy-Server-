package worlds

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func meta(name, seed string) []byte {
	return BuildMeta(Meta{Version: 35, Name: name, SeedName: seed, Seed: 12345, UID: 99}, true)
}

// makeFolderWorld writes a Valheim 1.0 style world folder.
func makeFolderWorld(t *testing.T, dir, name, seed string) {
	t.Helper()
	makeFolderWorldAs(t, dir, name, name, seed)
}

// makeFolderWorldAs writes a world folder whose files say it is called
// name, like Valheim's backup copies.
func makeFolderWorldAs(t *testing.T, dir, folder, name, seed string) {
	t.Helper()
	w := filepath.Join(dir, folder)
	writeFile(t, filepath.Join(w, "_main.1.fwl2"), meta(name, seed))
	writeFile(t, filepath.Join(w, "_main.1.db2"), bytes.Repeat([]byte{1}, 1000))
	writeFile(t, filepath.Join(w, "_main.1.chunks"), []byte("index"))
	writeFile(t, filepath.Join(w, "_main.1.ok"), []byte("ok"))
	writeFile(t, filepath.Join(w, "00_00__0_1.chunk"), bytes.Repeat([]byte{2}, 500))
}

func makeClassicWorld(t *testing.T, dir, base, name, seed string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, base+".fwl"), meta(name, seed))
	writeFile(t, filepath.Join(dir, base+".db"), bytes.Repeat([]byte{3}, 700))
}

func newLib(t *testing.T) *Library {
	t.Helper()
	root := t.TempDir()
	return &Library{
		Worlds:  filepath.Join(root, "saves", "worlds_local"),
		Backups: filepath.Join(root, "backups"),
		Staging: filepath.Join(root, "uploads"),
	}
}

func TestMetaRoundTripAndRename(t *testing.T) {
	data := meta("Midgard", "AbCdEf1234")
	m, err := ParseMeta(data)
	if err != nil || m.Name != "Midgard" || m.SeedName != "AbCdEf1234" || m.Seed != 12345 || m.UID != 99 {
		t.Fatalf("ParseMeta = %+v, %v", m, err)
	}
	renamed, err := RenameMeta(data, "Asgard Reborn")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := ParseMeta(renamed)
	if err != nil || m2.Name != "Asgard Reborn" || m2.SeedName != "AbCdEf1234" || m2.UID != 99 {
		t.Fatalf("renamed = %+v, %v", m2, err)
	}
	// Everything after the name must be unchanged.
	if !bytes.HasSuffix(renamed, data[4+4+1+len("Midgard"):]) {
		t.Fatal("bytes after the name changed")
	}
	if _, err := ParseMeta([]byte("not a world")); err == nil {
		t.Fatal("garbage parsed as a world")
	}
}

func TestListBothFormatsAndBackups(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "SeedOne")
	// Valheim 1.0 auto backup folder and the leftovers of a conversion.
	makeFolderWorld(t, l.Worlds, "Midgard_backup_auto-20260912-173107", "SeedOne")
	makeClassicWorld(t, l.Worlds, "Midgard_backup_20260909-134827", "Midgard", "SeedOne")
	writeFile(t, filepath.Join(l.Worlds, "Midgard.fwl.old"), meta("Midgard", "SeedOne"))
	writeFile(t, filepath.Join(l.Worlds, "Midgard.db.old"), []byte("old"))
	// A classic world, not yet converted, with a classic auto backup.
	makeClassicWorld(t, l.Worlds, "Dedicated", "Dedicated", "SeedTwo")
	makeClassicWorld(t, l.Worlds, "Dedicated_backup_auto-20230209191710", "Dedicated", "SeedTwo")
	// A world that only has its .fwl (created but never saved).
	writeFile(t, filepath.Join(l.Worlds, "Fresh.fwl"), meta("Fresh", "SeedThree"))
	// A stray folder that isn't a world.
	writeFile(t, filepath.Join(l.Worlds, "notes", "readme.txt"), []byte("hi"))

	worlds, orphans, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("orphans = %+v", orphans)
	}
	got := map[string]World{}
	for _, w := range worlds {
		got[w.Name] = w
	}
	if len(got) != 3 {
		t.Fatalf("worlds = %+v", worlds)
	}
	mid := got["Midgard"]
	if mid.Format != FormatFolder || mid.Seed != "SeedOne" || mid.Size != int64(len(meta("Midgard", "SeedOne"))+1000+5+2+500) {
		t.Fatalf("Midgard = %+v", mid)
	}
	if len(mid.Backups) != 3 {
		t.Fatalf("Midgard backups = %+v", mid.Backups)
	}
	if mid.Backups[0].Kind != KindPrevious && mid.Backups[0].Time.Year() != 2026 {
		t.Fatalf("backups not sorted newest first: %+v", mid.Backups)
	}
	kinds := map[string]bool{}
	for _, b := range mid.Backups {
		kinds[b.Kind] = true
	}
	if !kinds[KindAuto] || !kinds[KindValheim] || !kinds[KindPrevious] {
		t.Fatalf("backup kinds = %v", kinds)
	}
	ded := got["Dedicated"]
	if ded.Format != FormatClassic || ded.Seed != "SeedTwo" || len(ded.Backups) != 1 || ded.Backups[0].Kind != KindAuto {
		t.Fatalf("Dedicated = %+v", ded)
	}
	if want := time.Date(2023, 2, 9, 19, 17, 10, 0, time.Local); !ded.Backups[0].Time.Equal(want) {
		t.Fatalf("backup time = %v, want %v", ded.Backups[0].Time, want)
	}
	if !got["Fresh"].NeverSaved {
		t.Fatal("Fresh should be marked never saved")
	}
}

func TestFolderWinsOverClassicPair(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "S")
	makeClassicWorld(t, l.Worlds, "Midgard", "Midgard", "S")
	worlds, _, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(worlds) != 1 || worlds[0].Format != FormatFolder || !worlds[0].Shadowed {
		t.Fatalf("worlds = %+v", worlds)
	}
}

func zipEntries(t *testing.T, data []byte) map[string]int {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, f := range zr.File {
		out[f.Name] = int(f.UncompressedSize64)
	}
	return out
}

func TestExportAndBackupDeleteRestore(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "S")

	path, err := l.ExportFile("Midgard", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	entries := zipEntries(t, data)
	if entries["Midgard/_main.1.db2"] != 1000 || entries["Midgard/00_00__0_1.chunk"] != 500 || entries["How to play this world.txt"] == 0 || len(entries) != 6 {
		t.Fatalf("export entries = %v", entries)
	}
	// What was downloaded can be added again.
	u, _ := l.NewUpload()
	defer u.Remove()
	f, _ := os.Open(path)
	err = u.AddFile("Midgard 2026-10-23.zip", f)
	f.Close()
	if cands := u.Scan(l); err != nil || len(cands) != 1 || cands[0].Name != "Midgard" {
		t.Fatalf("re-adding the download: %v %+v", err, cands)
	}

	if _, err := l.Backup("Midgard", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete("Midgard"); err != nil {
		t.Fatal(err)
	}
	worlds, orphans, _ := l.List()
	if len(worlds) != 0 || len(orphans) != 1 || len(orphans[0].Backups) != 2 {
		t.Fatalf("after delete: worlds=%+v orphans=%+v", worlds, orphans)
	}
	// Restore the "deleted" copy.
	var deleted Backup
	for _, b := range orphans[0].Backups {
		if strings.Contains(b.ID, "_deleted_") {
			deleted = b
		}
	}
	if deleted.ID == "" {
		t.Fatalf("no deleted copy in %+v", orphans[0].Backups)
	}
	if err := l.Restore("Midgard", deleted.ID); err != nil {
		t.Fatal(err)
	}
	w, err := l.Get("Midgard")
	if err != nil || w.Format != FormatFolder || w.Seed != "S" {
		t.Fatalf("restored world = %+v, %v", w, err)
	}
	if _, err := os.Stat(filepath.Join(l.Worlds, "Midgard", "_main.1.db2")); err != nil {
		t.Fatal("restored files missing")
	}
}

func TestRestoreValheimAutoBackupReplacesFolder(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "Now")
	writeFile(t, filepath.Join(l.Worlds, "Midgard", "extra_new.chunk"), []byte("x"))
	makeFolderWorld(t, l.Worlds, "Midgard_backup_auto-20260912-173107", "Then")

	w, _ := l.Get("Midgard")
	var auto Backup
	for _, b := range w.Backups {
		if b.Kind == KindAuto {
			auto = b
		}
	}
	if err := l.Restore("Midgard", auto.ID); err != nil {
		t.Fatal(err)
	}
	w, _ = l.Get("Midgard")
	if w.Seed != "Then" {
		t.Fatalf("seed after restore = %q", w.Seed)
	}
	// Chunks from the replaced world must not be mixed in.
	if _, err := os.Stat(filepath.Join(l.Worlds, "Midgard", "extra_new.chunk")); err == nil {
		t.Fatal("old chunk file left behind")
	}
	// The world that was replaced is kept as an app backup.
	found := false
	for _, b := range w.Backups {
		if b.Kind == KindApp && strings.Contains(b.ID, "before-restore") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no before-restore backup: %+v", w.Backups)
	}
}

func TestRenameClassicWorld(t *testing.T) {
	l := newLib(t)
	makeClassicWorld(t, l.Worlds, "Dedicated", "Dedicated", "S")
	if err := l.Rename("Dedicated", "Bjorns World"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(l.Worlds, "Bjorns World.fwl"))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := ParseMeta(data); m.Name != "Bjorns World" {
		t.Fatalf("name inside .fwl = %q", m.Name)
	}
	if fileExists(filepath.Join(l.Worlds, "Dedicated.fwl")) || !fileExists(filepath.Join(l.Worlds, "Bjorns World.db")) {
		t.Fatal("files not renamed")
	}
	makeFolderWorld(t, l.Worlds, "Midgard", "S")
	if err := l.Rename("Midgard", "Other"); err == nil {
		t.Fatal("renaming a 1.0 world should be refused")
	}
}

func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	zw.Close()
	return buf.Bytes()
}

func TestUploadZipAndImport(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "Existing")

	z := buildZip(t, map[string][]byte{
		"worlds_local/Midgard/_main.3.fwl2":       meta("Midgard", "Uploaded"),
		"worlds_local/Midgard/_main.3.db2":        []byte("db"),
		"worlds_local/Midgard/01_01__0_3.chunk":   []byte("c"),
		"worlds_local/Dedicated.fwl":              meta("Dedicated", "Old"),
		"worlds_local/Dedicated.db":               []byte("olddb"),
		"worlds_local/Dedicated_backup_auto-1.db": []byte("skip me? no: no .fwl"),
	})
	u, err := l.NewUpload()
	if err != nil {
		t.Fatal(err)
	}
	defer u.Remove()
	if err := u.AddFile("worlds.zip", bytes.NewReader(z)); err != nil {
		t.Fatal(err)
	}
	cands := u.Scan(l)
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v", cands)
	}
	var folder, classic Candidate
	for _, c := range cands {
		if c.Format == FormatFolder {
			folder = c
		} else {
			classic = c
		}
	}
	if folder.Name != "Midgard" || !folder.Exists || folder.Renamable || folder.Seed != "Uploaded" {
		t.Fatalf("folder candidate = %+v", folder)
	}
	if classic.Name != "Dedicated" || classic.Exists || !classic.Renamable {
		t.Fatalf("classic candidate = %+v", classic)
	}

	// Without "replace" an existing world is refused.
	if _, err := l.Import(cands, []Choice{{ID: folder.ID, Action: "import"}}, func(string) bool { return false }, true); err == nil {
		t.Fatal("import over an existing world without replace should fail")
	}
	// Replacing a world the server is running is refused.
	if _, err := l.Import(cands, []Choice{{ID: folder.ID, Action: "replace"}}, func(string) bool { return true }, true); err == nil {
		t.Fatal("replacing a running world should fail")
	}
	done, err := l.Import(cands, []Choice{
		{ID: folder.ID, Action: "replace"},
		{ID: classic.ID, Action: "import", Name: "Old Times"},
	}, func(string) bool { return false }, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || done[0] != (Placed{"Midgard", true}) || done[1] != (Placed{"Old Times", false}) {
		t.Fatalf("imported = %+v", done)
	}
	mid, _ := l.Get("Midgard")
	if mid.Seed != "Uploaded" {
		t.Fatalf("Midgard not replaced: %+v", mid)
	}
	if _, err := os.Stat(filepath.Join(l.Worlds, "Midgard", "_main.1.db2")); err == nil {
		t.Fatal("files from the replaced world were mixed in")
	}
	old, err := l.Get("Old Times")
	if err != nil || old.Format != FormatClassic {
		t.Fatalf("Old Times = %+v, %v", old, err)
	}
	data, _ := os.ReadFile(filepath.Join(l.Worlds, "Old Times.fwl"))
	if m, _ := ParseMeta(data); m.Name != "Old Times" {
		t.Fatalf("internal name = %q", m.Name)
	}
}

func TestUploadLooseFolderFiles(t *testing.T) {
	l := newLib(t)
	u, err := l.NewUpload()
	if err != nil {
		t.Fatal(err)
	}
	defer u.Remove()
	// A browser folder upload sends relative paths like this.
	u.AddFile("Asgard/_main.0.fwl2", bytes.NewReader(meta("Asgard", "X")))
	u.AddFile("Asgard/_main.0.db2", bytes.NewReader([]byte("db")))
	cands := u.Scan(l)
	if len(cands) != 1 || cands[0].Name != "Asgard" || cands[0].Format != FormatFolder {
		t.Fatalf("candidates = %+v", cands)
	}
}

func TestZipSlipRejected(t *testing.T) {
	l := newLib(t)
	u, _ := l.NewUpload()
	defer u.Remove()
	z := buildZip(t, map[string][]byte{"../../evil.txt": []byte("x")})
	if err := u.AddFile("evil.zip", bytes.NewReader(z)); err == nil {
		t.Fatal("zip with ../ path accepted")
	}
	if err := u.AddFile("../escape.fwl", bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("upload path with ../ accepted")
	}
	if err := u.AddFile(`C:\Windows\evil.fwl`, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("absolute Windows path accepted")
	}
}

func TestLocalCandidates(t *testing.T) {
	l := newLib(t)
	home := t.TempDir()
	src := GameSaveSources(home, "", true)
	makeFolderWorld(t, src[0].Dir, "MyGame", "S")
	makeClassicWorld(t, src[1].Dir, "Older", "Older", "S")
	cands := l.LocalCandidates(src)
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v", cands)
	}
	done, err := l.Import(cands, []Choice{{ID: cands[0].ID, Action: "import"}, {ID: cands[1].ID, Action: "import"}},
		func(string) bool { return false }, false)
	if err != nil || len(done) != 2 {
		t.Fatalf("import = %v, %v", done, err)
	}
	// Copying must leave the game's own saves in place.
	if _, err := os.Stat(filepath.Join(src[0].Dir, "MyGame", "_main.1.fwl2")); err != nil {
		t.Fatal("source world was moved instead of copied")
	}
}

func TestOneCopyPerWorld(t *testing.T) {
	l := newLib(t)
	u, err := l.NewUpload()
	if err != nil {
		t.Fatal(err)
	}
	defer u.Remove()
	// Someone uploads their whole worlds_local folder: the world, Valheim's
	// backups of it, an old-format copy, and a world that only has backups.
	makeFolderWorld(t, u.Content, "Midgard", "Main")
	makeFolderWorldAs(t, u.Content, "Midgard_backup_auto-20240101120000", "Midgard", "Backup1")
	makeFolderWorldAs(t, u.Content, "Midgard_backup_auto-20240202120000", "Midgard", "Backup2")
	makeClassicWorld(t, u.Content, "Midgard", "Midgard", "Classic")
	makeFolderWorldAs(t, u.Content, "Solo_backup_auto-20240101120000", "Solo", "Older")
	makeFolderWorldAs(t, u.Content, "Solo_backup_auto-20240301120000", "Solo", "Newer")
	for _, f := range []string{"_main.1.fwl2", "_main.1.db2", "_main.1.chunks", "_main.1.ok", "00_00__0_1.chunk"} {
		old := time.Now().Add(-48 * time.Hour)
		os.Chtimes(filepath.Join(u.Content, "Solo_backup_auto-20240101120000", f), old, old)
	}
	cands := u.Scan(l)
	kept := map[string]string{}
	for _, c := range cands {
		if !c.Superseded {
			if _, dup := kept[c.Name]; dup {
				t.Fatalf("two copies of %s kept: %+v", c.Name, cands)
			}
			kept[c.Name] = c.Seed
		}
	}
	if len(cands) != 6 || kept["Midgard"] != "Main" || kept["Solo"] != "Newer" {
		t.Fatalf("kept %v from %+v", kept, cands)
	}
	var choices []Choice
	for _, c := range cands {
		action := "import"
		if c.Superseded {
			action = "skip"
		}
		choices = append(choices, Choice{ID: c.ID, Action: action})
	}
	if _, err := l.Import(cands, choices, func(string) bool { return false }, true); err != nil {
		t.Fatal(err)
	}
	if w, err := l.Get("Solo"); err != nil || w.Seed != "Newer" {
		t.Fatalf("Solo = %+v, %v", w, err)
	}
}

func TestNameClashDetails(t *testing.T) {
	l := newLib(t)
	makeClassicWorld(t, l.Worlds, "Old", "Old", "Server")
	makeClassicWorld(t, l.Worlds, "Old 2", "Old 2", "Server")
	makeFolderWorld(t, l.Worlds, "Midgard", "Server")
	u, _ := l.NewUpload()
	defer u.Remove()
	makeClassicWorld(t, u.Content, "Old", "Old", "Mine")
	makeFolderWorld(t, u.Content, "Midgard", "Mine")
	byName := map[string]Candidate{}
	for _, c := range u.Scan(l) {
		byName[c.Name] = c
	}
	old, mid := byName["Old"], byName["Midgard"]
	if !old.Exists || old.FreeName != "Old 3" || old.ServerModified.IsZero() || old.ServerSize == 0 {
		t.Fatalf("classic clash = %+v", old)
	}
	if !mid.Exists || mid.FreeName != "" || mid.ServerModified.IsZero() {
		t.Fatalf("1.0 clash = %+v", mid)
	}
	// Keeping both: the upload goes in under the free name.
	done, err := l.Import(u.Candidates, []Choice{{ID: old.ID, Action: "import", Name: old.FreeName}, {ID: mid.ID, Action: "skip"}},
		func(string) bool { return false }, true)
	if err != nil || len(done) != 1 || done[0].Name != "Old 3" {
		t.Fatalf("keep both = %+v, %v", done, err)
	}
	if w, _ := l.Get("Old"); w.Seed != "Server" {
		t.Fatal("the server's copy was changed")
	}
}

func TestFailedReplacePutsOldCopyBack(t *testing.T) {
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "Server")
	u, _ := l.NewUpload()
	defer u.Remove()
	makeFolderWorld(t, u.Content, "Midgard", "Upload")
	cands := u.Scan(l)
	// The uploaded files vanish before they can be placed.
	os.RemoveAll(filepath.Join(u.Content, "Midgard"))
	_, err := l.Import(cands, []Choice{{ID: cands[0].ID, Action: "replace"}}, func(string) bool { return false }, true)
	if err == nil || !strings.Contains(err.Error(), "left as it was") {
		t.Fatalf("err = %v", err)
	}
	w, err := l.Get("Midgard")
	if err != nil || w.Seed != "Server" {
		t.Fatalf("Midgard after failed replace = %+v, %v", w, err)
	}
	if _, err := os.Stat(filepath.Join(l.Worlds, "Midgard", "00_00__0_1.chunk")); err != nil {
		t.Fatal("old copy not fully restored")
	}
}

func TestExportWaitsForSaves(t *testing.T) {
	old := exportRetryDelay
	exportRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { exportRetryDelay, exportCopied = old, nil })
	l := newLib(t)
	makeFolderWorld(t, l.Worlds, "Midgard", "S")
	// The "server" saves while each copy is being zipped, so no
	// half-and-half copy may be handed out. Each save has a new size, which
	// shows even where file times are coarse.
	saves := 0
	exportCopied = func() {
		saves++
		os.WriteFile(filepath.Join(l.Worlds, "Midgard", "_main.1.db2"), bytes.Repeat([]byte{byte(saves)}, 1000+saves), 0o644)
	}
	_, err := l.ExportFile("Midgard", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "kept saving") || saves != 3 {
		t.Fatalf("err = %v after %d saves", err, saves)
	}
	// Once it settles, the export works.
	exportCopied = nil
	if _, err := l.ExportFile("Midgard", t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// Names that can never be made into a free one (a file called "..fwl" in an
// upload, or one with "_backup_" in it) don't hang the check.
func TestCheckWithHopelessNames(t *testing.T) {
	l := newLib(t)
	writeFile(t, filepath.Join(l.Worlds, "x_backup_y.fwl"), meta("x_backup_y", "AbCdEf1234"))
	done := make(chan []Candidate, 1)
	go func() {
		done <- l.Check([]Candidate{{Name: ".", Renamable: true}, {Name: "x_backup_y", Renamable: true}})
	}()
	select {
	case cands := <-done:
		for _, c := range cands {
			if c.FreeName != "" {
				t.Errorf("%q got free name %q", c.Name, c.FreeName)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check didn't finish")
	}
}

// A metadata file too big to be one isn't read into memory.
func TestReadMetaLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.fwl")
	f, _ := os.Create(path)
	f.Truncate(maxMeta + 1)
	f.Close()
	if _, err := readMeta(path); err == nil {
		t.Fatal("read a file bigger than any world's .fwl")
	}
	writeFile(t, path, meta("Midgard", "AbCdEf1234"))
	if data, err := readMeta(path); err != nil {
		t.Fatal(err)
	} else if m, err := ParseMeta(data); err != nil || m.Name != "Midgard" {
		t.Fatalf("meta = %+v %v", m, err)
	}
}
