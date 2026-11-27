package gametest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// DataEnv names the environment variable that says where sessions keep
// their data folders: the instance's server, saves and logs, and the
// shared SteamCMD.
//
// Without it, a session's data folder is the test's own temporary folder
// (t.TempDir), which the testing package removes when the test ends,
// passed or failed. That suits a developer's computer, but the first
// real-server run on GitHub showed what it costs there: every failed cell
// uploaded nothing, because the servers' logs had gone with the folder
// before the workflow could collect them. So real-server.yml sets it, and
// a session's folder under it is removed when its test passes and kept
// when it fails, for the workflow to collect the logs from. It is kept
// too when the test run is cut off by go test's -timeout, when no
// clean-up runs at all.
//
// The workflow's folder also lies on the runner's biggest drive (D: on
// Windows, where C: has much less free space), and its paths are shorter
// than the temporary folder's, which helps on Windows, where some servers
// can't use paths longer than 260 characters.
const DataEnv = "EGS_TEST_DATA"

// DataDir makes a new, empty data folder for one test: under the folder
// DataEnv names when it is set (removed if the test passes, kept if it
// fails), otherwise the test's own temporary folder. Every Session's data
// folder comes from it; a test that runs a real server some other way
// (the manager's Valheim check, say) can use it too, so its logs are
// collected the same way.
func DataDir(t testing.TB) string {
	t.Helper()
	root := os.Getenv(DataEnv)
	if root == "" {
		return t.TempDir()
	}
	// Servers are started in folders of their own, so a relative path
	// would point somewhere else for them.
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("gametest: %s=%q: %v", DataEnv, os.Getenv(DataEnv), err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("gametest: making %s's folder: %v", DataEnv, err)
	}
	dir, err := os.MkdirTemp(root, folderName(t.Name())+"-")
	if err != nil {
		t.Fatalf("gametest: making a data folder in %s: %v", root, err)
	}
	// Which test the folder belongs to, since its name is cut short, for
	// whoever reads the collected logs.
	note := fmt.Sprintf("Data folder of the test %s, made %s.\n", t.Name(), time.Now().Format(time.RFC3339))
	os.WriteFile(filepath.Join(dir, "gametest.txt"), []byte(note), 0o644)
	t.Cleanup(func() {
		// This runs after the session's own clean-up (clean-ups run last
		// first), which has stopped any server still running in it.
		if t.Failed() {
			t.Logf("gametest: the test failed, so its data folder is kept for its logs: %s", dir)
			return
		}
		removeFolder(t, dir)
	})
	return dir
}

// removeFolder removes a passed test's data folder. Windows can refuse for
// a moment after a program in it has ended, while the system lets go of
// its files, so it tries a few times. A folder it still can't remove fails
// the test, as t.TempDir's clean-up does: something is still writing
// there or holding its files open, which usually means the server left a
// process of its own running (spec 2.4 asks adapters to start the server
// itself, never a launcher). The message names what is left, to show
// which program it was.
func removeFolder(t testing.TB, dir string) {
	var err error
	for try := 0; try < 5; try++ {
		if try > 0 {
			time.Sleep(time.Second)
		}
		if err = os.RemoveAll(dir); err == nil {
			return
		}
	}
	t.Errorf("gametest: couldn't remove the data folder %s after the test (is a program still using it?): %v\nwhat is left: %s", dir, err, leftIn(dir))
}

// leftIn names up to ten files and folders still in dir.
func leftIn(dir string) string {
	var left []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if len(left) == 10 {
			left = append(left, "…")
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(dir, p)
		left = append(left, rel)
		return nil
	})
	if len(left) == 0 {
		return "nothing (it was being written to while it was removed)"
	}
	return strings.Join(left, ", ")
}

// folderName is a short, safe folder name for a test: the last part of its
// name, in letters, digits, dots, dashes and underscores only. Servers'
// own scripts and Windows batch files can trip over spaces, commas and
// the like in a path.
func folderName(test string) string {
	if i := strings.LastIndex(test, "/"); i >= 0 {
		test = test[i+1:]
	}
	b := []byte(test)
	for i, c := range b {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '.', c == '-', c == '_':
		default:
			b[i] = '_'
		}
	}
	name := strings.Trim(string(b), "._")
	if len(name) > 24 {
		name = strings.TrimRight(name[:24], "._")
	}
	if name == "" {
		name = "test"
	}
	return name
}
