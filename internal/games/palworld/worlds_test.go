package palworld

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
)

const (
	idA = "C8A34115A3414E9E8EE21C5D7BC7391E"
	idB = "36B882106A4F4EBCAA16940C167F791E"
	idC = "0123456789ABCDEF0123456789ABCDEF"
)

// makeWorld lays out a world folder as the server does: Level.sav,
// LevelMeta.sav, a player, and the server's own backup copies.
func makeWorld(t *testing.T, s *server, id string, size int) {
	t.Helper()
	dir := filepath.Join(s.savesDir(), id)
	for _, d := range []string{filepath.Join(dir, "Players"), filepath.Join(dir, "backup", "world", "2026.11.26-20.00.00")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, levelFile), make([]byte, size), 0o644)
	os.WriteFile(filepath.Join(dir, "LevelMeta.sav"), []byte("meta"), 0o644)
	os.WriteFile(filepath.Join(dir, "Players", "AFAFD830000000000000000000000000.sav"), []byte("player"), 0o644)
	os.WriteFile(filepath.Join(dir, "backup", "world", "2026.11.26-20.00.00", levelFile), make([]byte, 50000), 0o644)
}

func setState(s *server, kv ...string) {
	s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		for i := 0; i+1 < len(kv); i += 2 {
			sv.State[kv[i]] = kv[i+1]
		}
		return nil
	})
}

// Worlds are listed by the names in State; a folder the app didn't name
// is listed under its id; a folder without Level.sav (nothing saved yet)
// isn't a world yet. Sizes leave out the server's own backups.
func TestWorldsList(t *testing.T) {
	s := testServer(t)
	makeWorld(t, s, idA, 1000)
	makeWorld(t, s, idB, 2000)
	os.MkdirAll(filepath.Join(s.savesDir(), idC), 0o755) // nothing saved yet
	os.MkdirAll(filepath.Join(s.savesDir(), ".egs-tmp"), 0o755)
	setState(s, statePrefix+idA, "Island", statePrefix+idC, "Valley")

	list, _, err := s.Worlds().List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range list {
		names = append(names, w.Name)
		if w.Name == "Island" && w.Size != 1000+4+6 {
			t.Errorf("Island's size = %d, want its own files without the server's backups", w.Size)
		}
	}
	if !slices.Equal(names, []string{idB, "Island"}) {
		t.Errorf("worlds = %q", names)
	}
	if !s.Worlds().Exists("Island") || s.Worlds().Exists("Valley") || !s.Worlds().Exists(idB) {
		t.Error("Exists disagrees with the list")
	}
}

// A backup holds the world but not the server's own copies; restoring it
// brings the world back as it was; deleting the world removes its folder
// and keeps a backup, from which it comes back under its name.
func TestWorldsBackupRestoreDelete(t *testing.T) {
	s := testServer(t)
	makeWorld(t, s, idA, 1000)
	setState(s, statePrefix+idA, "Island")
	w := s.Worlds()

	b, err := w.Backup("Island", "test")
	if err != nil {
		t.Fatal(err)
	}
	zips, _ := filepath.Glob(filepath.Join(s.env.Dirs.Backups, "Island", "*.zip"))
	if len(zips) != 1 {
		t.Fatalf("backups: %q", zips)
	}
	if st, _ := os.Stat(zips[0]); st.Size() > 20000 {
		t.Errorf("the backup is %d bytes: the server's own copies went into it", st.Size())
	}

	os.WriteFile(filepath.Join(s.savesDir(), idA, levelFile), make([]byte, 3000), 0o644)
	if err := w.Restore("Island", b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Get("Island"); got.Size != 1010 {
		t.Errorf("after the restore the world has %d bytes", got.Size)
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), idA, "backup", "world")); err != nil {
		t.Errorf("the restore removed the server's own copies: %v", err)
	}

	if err := w.Delete("Island"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), idA)); !os.IsNotExist(err) {
		t.Errorf("the world's folder is still there: %v", err)
	}
	if w.Exists("Island") {
		t.Error("Island still exists")
	}
	_, orphans, _ := w.List()
	if len(orphans) != 1 || orphans[0].Name != "Island" {
		t.Fatalf("orphans = %+v", orphans)
	}
	deleted := ""
	for _, b := range orphans[0].Backups {
		if strings.HasSuffix(b.ID, "_deleted.zip") {
			deleted = b.ID
		}
	}
	if err := w.Restore("Island", deleted); err != nil {
		t.Fatal(err)
	}
	if got, err := w.Get("Island"); err != nil || got.Size != 1010 {
		t.Errorf("restoring the deleted world: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(s.savesDir(), idA, levelFile)); err != nil {
		t.Errorf("it isn't back in its own folder: %v", err)
	}
	if err := w.Rename("Island", "Other"); err == nil {
		t.Error("Rename worked; Palworld's worlds can't be renamed here")
	}
}

// A name that looks like one of the server's ids is refused, as are the
// shared rules' names.
func TestWorldNames(t *testing.T) {
	s := testServer(t)
	for _, bad := range []string{idA, strings.ToLower(idB), "", "a/b", "-x"} {
		if s.Worlds().NameProblem(bad) == "" {
			t.Errorf("%q was taken", bad)
		}
	}
	if p := s.Worlds().NameProblem("My Island 2"); p != "" {
		t.Error(p)
	}
}

// If the server made the last new world under an id of its own (the app's
// id never got a folder, and GameUserSettings.ini names another), the next
// plan gives that world the name.
func TestPlanAdoptsTheServersOwnID(t *testing.T) {
	s := testServer(t)
	sv := defaultSaved(s, "Island")
	setState(s, statePrefix+idC, "Island", statePlanned, idC)
	makeWorld(t, s, idB, 500)
	os.MkdirAll(s.configDir(), 0o755)
	os.WriteFile(s.userSettingsPath(), []byte("[/Script/Pal.PalGameLocalSettings]\r\nDedicatedServerName="+idB+"\r\n"), 0o644)

	if !s.Worlds().Exists("Island") {
		// Before the plan the world is listed under its id.
		if !s.Worlds().Exists(idB) {
			t.Fatal("the server's world isn't listed")
		}
	}
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: "Island", Saved: sv}); err != nil {
		t.Fatal(err)
	}
	st := s.store.Get().State
	if st[statePrefix+idB] != "Island" || st[statePrefix+idC] != "" || st[statePlanned] != idB {
		t.Errorf("State = %v", st)
	}
	if got := s.currentWorldChoice(); got != idB {
		t.Errorf("GameUserSettings.ini names %q", got)
	}
	if got := worldNames(t, s.Worlds()); !slices.Equal(got, []string{"Island"}) {
		t.Errorf("worlds = %q", got)
	}
}

// A world the owner copied in by hand (listed under its id) can be
// started by that id.
func TestPlanUnnamedWorld(t *testing.T) {
	s := testServer(t)
	makeWorld(t, s, idA, 100)
	sv := defaultSaved(s, idA)
	if _, err := s.Plan(context.Background(), game.PlanRequest{World: idA, Saved: sv}); err != nil {
		t.Fatal(err)
	}
	if got := s.currentWorldChoice(); got != idA {
		t.Errorf("GameUserSettings.ini names %q", got)
	}
}
