package gametest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataDirKeptOnFailure: with EGS_TEST_DATA set, a test's data folder
// is made there, removed when the test passes and kept when it fails, so
// real-server.yml can upload a failed real server's logs.
func TestDataDirKeptOnFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv(DataEnv, root)

	var passed, failed string
	t.Run("passes", func(t *testing.T) {
		passed = DataDir(t)
		if err := os.WriteFile(filepath.Join(passed, "server.log"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fails", func(t *testing.T) {
		rt := &recordingT{TB: t} // fails without failing this test
		failed = DataDir(rt)
		if err := os.WriteFile(filepath.Join(failed, "server.log"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt.Errorf("the server didn't start")
	})

	for name, dir := range map[string]string{"passes": passed, "fails": failed} {
		if filepath.Dir(dir) != root {
			t.Errorf("the %q test's data folder %s isn't in %s", name, dir, root)
		}
		if !strings.HasPrefix(filepath.Base(dir), name+"-") {
			t.Errorf("the %q test's data folder is named %q", name, filepath.Base(dir))
		}
	}
	if _, err := os.Stat(passed); !os.IsNotExist(err) {
		t.Errorf("the passed test's data folder is still there (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(failed, "server.log")); err != nil {
		t.Errorf("the failed test's data folder wasn't kept: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(failed, "gametest.txt")); err != nil || !strings.Contains(string(b), "TestDataDirKeptOnFailure/fails") {
		t.Errorf("the kept folder doesn't say which test it was (%q, %v)", b, err)
	}
}

// TestDataDirRelative: a relative EGS_TEST_DATA is made absolute, since
// servers start in folders of their own.
func TestDataDirRelative(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(DataEnv, "rel")
	dir := DataDir(t)
	if !filepath.IsAbs(dir) {
		t.Errorf("DataDir = %q, not an absolute path", dir)
	}
}

// TestDataDirUnset: without EGS_TEST_DATA, the test's own temporary
// folder, as before.
func TestDataDirUnset(t *testing.T) {
	t.Setenv(DataEnv, "")
	dir := DataDir(t)
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("DataDir without %s = %s, holding %v (%v); want a new, empty temporary folder", DataEnv, dir, entries, err)
	}
}

// TestDataRoot: DataRoot names the folder DataDir's folders are made in,
// set or not, so a real check's free-space note measures the drive its
// install lands on (D: on a Windows runner, not the temporary folder's C:).
func TestDataRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(DataEnv, "rel")
	root := DataRoot()
	if !filepath.IsAbs(root) {
		t.Errorf("DataRoot with a relative %s = %q, not an absolute path", DataEnv, root)
	}
	if dir := DataDir(t); filepath.Dir(dir) != root {
		t.Errorf("DataDir = %s, not in DataRoot %s", dir, root)
	}

	t.Setenv(DataEnv, "")
	if root, want := DataRoot(), filepath.Clean(os.TempDir()); root != want {
		t.Errorf("DataRoot without %s = %q; want the temporary folder %q", DataEnv, root, want)
	}
	// t.TempDir's folder is two levels down: its own folder for the
	// test, then a numbered one.
	if dir := DataDir(t); !strings.HasPrefix(dir, DataRoot()+string(filepath.Separator)) {
		t.Errorf("DataDir without %s = %s, not under DataRoot %s", DataEnv, dir, DataRoot())
	}
}

// TestLeftIn: a folder that couldn't be removed is described by what's in
// it, at most ten entries.
func TestLeftIn(t *testing.T) {
	dir := t.TempDir()
	if got := leftIn(dir); !strings.Contains(got, "nothing") {
		t.Errorf("leftIn(empty) = %q", got)
	}
	os.MkdirAll(filepath.Join(dir, "games", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "games", "x", "server.log"), nil, 0o644)
	if got, want := leftIn(dir), "games, "+filepath.Join("games", "x")+", "+filepath.Join("games", "x", "server.log"); got != want {
		t.Errorf("leftIn = %q, want %q", got, want)
	}
	for i := range 20 {
		os.WriteFile(filepath.Join(dir, "f"+strings.Repeat("x", i)), nil, 0o644)
	}
	if got := leftIn(dir); strings.Count(got, ", ") != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("leftIn of a full folder = %q, want ten entries and …", got)
	}
}

func TestFolderName(t *testing.T) {
	for name, want := range map[string]string{
		"TestRealServer":                                 "TestRealServer",
		"TestRealServer/conformance":                     "conformance",
		"TestRealServer/console,_takeover_and_Ctrl+C":    "console__takeover_and_Ct",
		"TestRealServer/settings,_saves,_takeover_and_x": "settings__saves__takeove",
		"TestX/..":         "test",
		"TestX/Code_✓":     "Code",
		"TestX/a.b-c_d":    "a.b-c_d",
		"TestX/__dots..__": "dots",
	} {
		if got := folderName(name); got != want {
			t.Errorf("folderName(%q) = %q, want %q", name, got, want)
		}
	}
}
