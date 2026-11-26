// Package steamcmdtest is a stand-in SteamCMD for tests, on every system.
//
// The test binary itself plays SteamCMD: Put copies it into a folder under
// SteamCMD's own name (steamcmd.exe on Windows, steamcmd.sh elsewhere), and
// Main, called from the package's TestMain, notices that name when the
// installer starts it and follows a small script instead of running the
// tests. So the real installer code (starting the program, reading its
// output, cancelling it) is what the tests exercise, on Windows too, where
// a shell script can't stand in.
//
// A script is one instruction per line:
//
//	any text          printed as a line on stdout
//	"quoted\r"        a Go-quoted string, printed exactly (no newline added)
//	@stderr text      printed as a line on stderr
//	@success          prints "Success! App '<id>' fully installed." for the app id it was asked for
//	@sleep 10m        waits (a time.Duration)
//	@touch name       creates the file name in the folder (to tell the test where it got to)
//	@await name       waits until the file name exists in the folder
//	@exit 3           ends with that exit code (the default is 0)
//
// Every run appends its arguments, as one JSON line, to the args file, so a
// test can check them (Args). A script named fake-steamcmd-<n>.txt is used
// for the n-th run when it exists, else fake-steamcmd.txt.
package steamcmdtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	scriptName = "fake-steamcmd.txt"
	argsName   = "fake-steamcmd-args.txt"
)

// ExeName is the name the installer looks for in its SteamCMD folder.
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "steamcmd.exe"
	}
	return "steamcmd.sh"
}

// Main runs the stand-in when this process was started as SteamCMD, and the
// tests otherwise. Call it from TestMain.
func Main(m *testing.M) {
	if IsFake() {
		os.Exit(run())
	}
	os.Exit(m.Run())
}

// IsFake reports whether this process was started as the stand-in SteamCMD.
func IsFake() bool {
	return strings.EqualFold(filepath.Base(os.Args[0]), ExeName())
}

// Put makes dir a SteamCMD folder whose SteamCMD is the stand-in running
// script, and returns the stand-in's path.
func Put(t testing.TB, dir, script string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, ExeName())
	if err := CopySelf(dst); err != nil {
		t.Fatal(err)
	}
	SetScript(t, dir, script)
	return dst
}

// CopySelf copies the running test binary to dst, runnable.
func CopySelf(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// SetScript replaces the script the stand-in in dir follows.
func SetScript(t testing.TB, dir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, scriptName), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SetScriptFor sets the script of the n-th run only (counting from 1).
func SetScriptFor(t testing.TB, dir string, n int, script string) {
	t.Helper()
	name := fmt.Sprintf("fake-steamcmd-%d.txt", n)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Args returns the arguments of every run so far, oldest first.
func Args(t testing.TB, dir string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, argsName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var runs [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, args)
	}
	return runs
}

// Await waits until the stand-in in dir has created name (with @touch).
func Await(t testing.TB, dir, name string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in SteamCMD never got to %q", name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// run is the stand-in itself.
func run() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 99
	}
	dir := filepath.Dir(exe)
	args := os.Args[1:]
	n, err := logArgs(filepath.Join(dir, argsName), args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 99
	}
	script, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("fake-steamcmd-%d.txt", n)))
	if err != nil {
		script, err = os.ReadFile(filepath.Join(dir, scriptName))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 99
	}
	appID := ""
	for i, a := range args {
		if a == "+app_update" && i+1 < len(args) {
			appID = args[i+1]
		}
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	sc := bufio.NewScanner(strings.NewReader(string(script)))
	for sc.Scan() {
		line := sc.Text()
		cmd, rest, _ := strings.Cut(line, " ")
		switch {
		case strings.HasPrefix(line, `"`):
			s, err := strconv.Unquote(line)
			if err != nil {
				s = line
			}
			fmt.Fprint(out, s)
		case cmd == "@stderr":
			out.Flush()
			fmt.Fprintln(os.Stderr, rest)
		case cmd == "@success":
			fmt.Fprintf(out, "Success! App '%s' fully installed.\n", appID)
		case cmd == "@sleep":
			out.Flush()
			d, _ := time.ParseDuration(rest)
			time.Sleep(d)
		case cmd == "@touch":
			out.Flush()
			os.WriteFile(filepath.Join(dir, rest), nil, 0o644)
		case cmd == "@await":
			out.Flush()
			// Give up after a while, so a failed test never leaves a
			// stand-in behind for long.
			for deadline := time.Now().Add(5 * time.Minute); ; {
				if _, err := os.Stat(filepath.Join(dir, rest)); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return 98
				}
				time.Sleep(10 * time.Millisecond)
			}
		case cmd == "@exit":
			out.Flush()
			code, _ := strconv.Atoi(rest)
			return code
		default:
			fmt.Fprintln(out, line)
		}
	}
	return 0
}

// logArgs appends one run's arguments and says which run this is.
func logArgs(path string, args []string) (int, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	all, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	return strings.Count(string(all), "\n"), nil
}
