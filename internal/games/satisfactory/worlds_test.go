package satisfactory

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// Worlds are sessions, grouped from the headers of their saves: the
// autosaves and the app's own save of a session make one world, a save
// imported under another file name is listed under the session inside it,
// and a file whose header can't be read is grouped by its file name.
func TestWorldsAreSessions(t *testing.T) {
	s := testServer(t, true)
	dir := s.savesDir()
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeTestSave(t, dir, "Factory", "Factory", base)
	writeTestSave(t, dir, "Factory_autosave_0", "Factory", base.Add(10*time.Minute))
	writeTestSave(t, dir, "Factory_autosave_1", "Factory", base.Add(5*time.Minute))
	writeTestSave(t, dir, "My upload", "Home Base", base)
	os.WriteFile(filepath.Join(dir, "Broken_autosave_2.sav"), []byte("not a save"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a save either"), 0o644)
	os.MkdirAll(filepath.Join(dir, "Folder.sav"), 0o755)

	list, _, err := s.worlds.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range list {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"Broken", "Factory", "Home Base"}) {
		t.Errorf("worlds = %q", names)
	}
	if save, ok := s.worlds.newestSave("factory"); !ok || save != "Factory_autosave_0" {
		t.Errorf("newest save of Factory = %q, %v", save, ok)
	}
	files, _ := s.worlds.files("Factory")
	if len(files) != 3 {
		t.Errorf("Factory's files = %q", files)
	}
	if got := s.worlds.sessionOf("My upload"); got != "Home Base" {
		t.Errorf("sessionOf(the upload's file name) = %q", got)
	}
	if got := s.worlds.sessionOf("factory"); got != "Factory" {
		t.Errorf("sessionOf(factory) = %q", got)
	}
	if got := s.worlds.sessionOf("Brand new"); got != "Brand new" {
		t.Errorf("sessionOf(a new world) = %q", got)
	}
	if !s.worlds.Exists("Factory") || !s.worlds.Exists("My upload") || s.worlds.Exists("Brand new") {
		t.Errorf("Exists: Factory %v, My upload %v, Brand new %v", s.worlds.Exists("Factory"), s.worlds.Exists("My upload"), s.worlds.Exists("Brand new"))
	}
	if err := s.worlds.Rename("Factory", "Other"); !errors.Is(err, game.ErrCantRename) {
		t.Errorf("Rename = %v", err)
	}
	if p := s.worlds.NameProblem("Base_autosave_3"); p == "" {
		t.Errorf("a name like an autosave is allowed")
	}
	if p := s.worlds.NameProblem("New Factory"); p != "" {
		t.Errorf("NameProblem(New Factory) = %q", p)
	}
}

// A backup holds every save of the session and puts them all back; a
// delete removes them all and nothing else.
func TestWorldBackupRestoreDelete(t *testing.T) {
	s := testServer(t, true)
	dir := s.savesDir()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeTestSave(t, dir, "Factory", "Factory", at)
	writeTestSave(t, dir, "Factory_autosave_0", "Factory", at.Add(time.Minute))
	writeTestSave(t, dir, "Other", "Other", at)
	b, err := s.worlds.Backup("Factory", "test")
	if err != nil {
		t.Fatal(err)
	}
	writeTestSave(t, dir, "Factory_autosave_1", "Factory", at.Add(2*time.Minute))
	os.WriteFile(filepath.Join(dir, "Factory.sav"), []byte("damaged"), 0o644)
	if err := s.worlds.Restore("Factory", b.ID); err != nil {
		t.Fatal(err)
	}
	files, _ := s.worlds.files("Factory")
	slices.Sort(files)
	if len(files) != 2 || filepath.Base(files[0]) != "Factory.sav" || filepath.Base(files[1]) != "Factory_autosave_0.sav" {
		t.Errorf("after the restore: %q", files)
	}
	if h, err := readSaveHeaderFile(filepath.Join(dir, "Factory.sav")); err != nil || h.SessionName != "Factory" {
		t.Errorf("Factory.sav after the restore: %+v, %v", h, err)
	}
	if err := s.worlds.Delete("Factory"); err != nil {
		t.Fatal(err)
	}
	if s.worlds.Exists("Factory") || !s.worlds.Exists("Other") {
		t.Errorf("after the delete: Factory %v, Other %v", s.worlds.Exists("Factory"), s.worlds.Exists("Other"))
	}
}

// The saves folder: the instance's own in fake mode, the Windows user's
// Local AppData for the real server (FACTS.md).
func TestSavesFolder(t *testing.T) {
	s := testServer(t, true)
	if want := filepath.Join(s.env.Dirs.Saves, "FactoryGame", "Saved", "SaveGames", "server"); s.savesDir() != want {
		t.Errorf("fake mode: %s, want %s", s.savesDir(), want)
	}
	if want := filepath.Join(s.env.Dirs.Saves, "FactoryGame", "Saved", "SaveGames", "ServerSettings.7777.sav"); s.settingsFile(7777) != want {
		t.Errorf("settings file: %s", s.settingsFile(7777))
	}
}
