package worldstore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"valheim-easy-server/internal/worlds"
)

// holdOpen opens a file the way a program that doesn't expect it to move
// does (a backup tool, a virus scanner, the game server): others may read
// and write it, but not delete or rename it, because FILE_SHARE_DELETE is
// left out. It returns a func that closes it.
func holdOpen(t *testing.T, path string) func() {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { syscall.CloseHandle(h) }) }
	t.Cleanup(release) // before t.TempDir's own cleanup, which can't delete an open file
	return release
}

// On Windows, a file of the world that another program holds open makes a
// restore, delete, rename or replace fail, and the world is left exactly as
// it was: never half moved.
func TestLockedFileOnWindows(t *testing.T) {
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
			if len(cands) != 1 {
				t.Fatalf("candidates = %+v", cands)
			}

			// A file deep in the world's folder, or its last file, so for
			// "files" the first file has already moved when the move fails.
			held := filepath.Join(e.live, "Alpha", "region", "r.0.0.mca")
			if l.kind == KindFiles {
				held = filepath.Join(e.live, "Alpha.wld.bak")
			}
			release := holdOpen(t, held)
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

			// Once the program lets go, the same changes work.
			release()
			if _, err := w.Import(cands, []worlds.Choice{{ID: cands[0].ID, Action: "replace"}}, never, true); err != nil {
				t.Fatal(err)
			}
			if err := w.Restore("Alpha", b.ID); err != nil {
				t.Fatal(err)
			}
			if err := w.Delete("Alpha"); err != nil {
				t.Fatal(err)
			}
			noHidden(t, e.live)
		})
	}
}
