//go:build !windows

package servers

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecretFilesPrivate (I9): servers.json holds Discord webhooks, so it,
// its backup and a damaged copy set aside are readable by the owner only.
// (Windows has no such permission bits; the data folder is the owner's.)
func TestSecretFilesPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	mode := func(p string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}

	if _, _, err := Open(dir, "valheim", nil, now); err != nil {
		t.Fatal(err)
	}
	if m := mode(path); m != 0o600 {
		t.Errorf("new servers.json is %o", m)
	}
	// A hand-made backup with loose permissions is replaced by a private one.
	writeFile(t, path+BackupSuffix, "{}")
	if err := Update(path, func(f *File) error { f.App.Stars = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + BackupSuffix} {
		if m := mode(p); m != 0o600 {
			t.Errorf("%s is %o after a change", filepath.Base(p), m)
		}
	}
	// A damaged list (written loosely by hand) is set aside privately, and
	// the backup written back is private.
	writeFile(t, path, `{"schema":1,"webhook":"https://discord.com/api/webhooks/1/secret"`)
	_, o, err := Open(dir, "valheim", nil, now)
	if err != nil || o.Kept == "" {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	for _, p := range []string{path, o.Kept} {
		if m := mode(p); m != 0o600 {
			t.Errorf("%s is %o", filepath.Base(p), m)
		}
	}
	// A rebuilt list too.
	os.Remove(path)
	os.Remove(path + BackupSuffix)
	os.MkdirAll(filepath.Join(dir, "games"), 0o755)
	if _, o, err := Open(dir, "valheim", nil, now); err != nil || !o.Rebuilt {
		t.Fatalf("Open = %+v, %v", o, err)
	}
	if m := mode(path); m != 0o600 {
		t.Errorf("rebuilt servers.json is %o", m)
	}
}
