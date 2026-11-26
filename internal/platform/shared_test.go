package platform

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// While the app follows a game's log through OpenShared, the game can
// still write to it and rotate it: rename it, start a new one in its
// place, and delete the old one. Windows refuses the rename and the
// delete unless the app opened the file allowing them.
func TestOpenSharedLetsTheGameRotate(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")
	if err := os.WriteFile(log, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenShared(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("the game can't write to its log: %v", err)
	}
	w.WriteString("second line\n")
	w.Close()
	if err := os.Rename(log, log+".1"); err != nil {
		t.Fatalf("the game can't rename its log while it is followed: %v", err)
	}
	if err := os.WriteFile(log, []byte("new log\n"), 0o644); err != nil {
		t.Fatalf("the game can't start a new log: %v", err)
	}
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "first line\nsecond line\n" {
		t.Fatalf("read %q, %v", b, err)
	}
	if err := os.Remove(log + ".1"); err != nil {
		t.Fatalf("the game can't delete its old log while it is followed: %v", err)
	}

	if _, err := OpenShared(filepath.Join(dir, "missing.log")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file gives %v", err)
	}
}
