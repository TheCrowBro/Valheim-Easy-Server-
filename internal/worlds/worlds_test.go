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
	w := filepath.Join(dir, name)
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

	var buf bytes.Buffer
	if err := l.Export(&buf, "Midgard"); err != nil {
		t.Fatal(err)
	}
	entries := zipEntries(t, buf.Bytes())
	if entries["Midgard/_main.1.db2"] != 1000 || entries["Midgard/00_00__0_1.chunk"] != 500 || len(entries) != 5 {
		t.Fatalf("export entries = %v", entries)
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
	if strings.Join(done, ",") != "Midgard,Old Times" {
		t.Fatalf("imported = %v", done)
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
