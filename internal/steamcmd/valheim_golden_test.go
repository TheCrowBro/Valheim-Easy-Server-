package steamcmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"valheim-easy-server/internal/steamcmd/steamcmdtest"
)

// These pin what Valheim's install looks like today (bed004b): the
// arguments SteamCMD gets and every text the owner can see. The multi-game
// work gives SteamCMD an app id, a name and a size, and Valheim's install
// must stay byte-identical when they are left empty (spec 2.3).

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may turn the file's line ends into \r\n.
	want := strings.ReplaceAll(string(b), "\r\n", "\n")
	if got != want {
		t.Errorf("%s changed.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// recorder keeps what an install showed: progress steps and SteamCMD's
// own output lines.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) progress(p Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf("progress\t%s\t%g", p.Step, p.Percent))
}

func (r *recorder) output(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, "output\t"+line)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n") + "\n"
}

// valheimTranscript is what SteamCMD prints for a first install: it
// updates itself, logs in and downloads the server.
const valheimTranscript = `Redirecting stderr to '/home/user/Steam/logs/stderr.txt'
[  0%] Checking for available updates...
[----] Verifying installation...
[  0%] Downloading update...
[ 45%] Downloading update (12,345 of 27,000 KB)...
[100%] Download complete.
[----] Extracting package...
[----] Installing update...
[----] Update complete, launching Steamcmd...
Steam Console Client (c) Valve Corporation - version 1716584580
-- type 'quit' to exit --
Loading Steam API...OK
Connecting anonymously to Steam Public...OK
Waiting for client config...OK
Waiting for user info...OK
" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)\r"
" Update state (0x11) preallocating, progress: 10.00 (100 / 1000)\r\n"
" Update state (0x61) downloading, progress: 12.34 (123456789 / 1000000000)\r"
" Update state (0x61) downloading, progress: 87.50 (875000000 / 1000000000)\n"
 Update state (0x81) verifying update, progress: 50.00 (500 / 1000)
 Update state (0x101) committing, progress: 99.00 (990 / 1000)
Success! App '896660' fully installed.
`

func TestValheimArgsGolden(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	steamcmdtest.Put(t, steamDir, "Success! App '896660' fully installed.\n")
	server := filepath.Join(dir, "server")
	s := &SteamCMD{Dir: steamDir, InstallDir: server}
	for _, validate := range []bool{false, true} {
		if err := s.Install(context.Background(), validate, func(Progress) {}); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{
		{"+force_install_dir", server, "+login", "anonymous", "+app_update", "896660", "+quit"},
		{"+force_install_dir", server, "+login", "anonymous", "+app_update", "896660", "validate", "+quit"},
	}
	if got := steamcmdtest.Args(t, steamDir); !reflect.DeepEqual(got, want) {
		t.Errorf("SteamCMD was run with\n%q\nwant\n%q", got, want)
	}
}

func TestValheimInstallTextsGolden(t *testing.T) {
	cases := []struct {
		name   string
		script string
		// second is the script of the retry, when the first run fails.
		second string
	}{
		{name: "first install", script: valheimTranscript},
		{name: "unconfirmed", script: "Loading Steam API...OK\nWaiting for user info...OK\n",
			second: "Loading Steam API...OK\nUnloading Steam API...OK\n"},
		{name: "crashed", script: "Loading Steam API...OK\n@exit 5\n",
			second: "Connecting anonymously to Steam Public...OK\n@exit 6\n"},
		{name: "disk full", script: "ERROR! Failed to install app '896660' (Disk space)\n",
			second: "Error! App '896660' state is 0x202 after update job.\n"},
		{name: "no connection", script: "ERROR! Timeout downloading item\n", second: "@success\n"},
		{name: "bad cpu", script: "@stderr steamcmd.sh: line 37: steamcmd: Bad CPU type in executable\n@exit 126\n",
			second: "@stderr steamcmd.sh: line 37: steamcmd: Bad CPU type in executable\n@exit 126\n"},
		{name: "another app's success", script: "Success! App '1169370' fully installed.\n",
			second: "Success! App '1169370' fully installed.\n"},
	}
	var out strings.Builder
	for _, c := range cases {
		dir := t.TempDir()
		steamDir := filepath.Join(dir, "steamcmd")
		steamcmdtest.Put(t, steamDir, c.script)
		if c.second != "" {
			steamcmdtest.SetScriptFor(t, steamDir, 2, c.second)
		}
		rec := &recorder{}
		s := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "server"), Output: rec.output}
		err := s.Install(context.Background(), false, rec.progress)
		fmt.Fprintf(&out, "== %s ==\n%s", c.name, rec)
		if err != nil {
			fmt.Fprintf(&out, "error\t%s\n", err)
		} else {
			out.WriteString("ok\n")
		}
	}
	golden(t, "valheim_install.golden", out.String())
}

func TestValheimTextsGolden(t *testing.T) {
	var out strings.Builder
	out.WriteString("== ParseProgress ==\n")
	for _, line := range []string{
		" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)",
		" Update state (0x11) preallocating, progress: 10.00 (100 / 1000)",
		" Update state (0x61) downloading, progress: 12.34 (123456789 / 1000000000)",
		" Update state (0x81) verifying update, progress: 50.00 (500 / 1000)",
		" Update state (0x5) verifying install, progress: 3.00 (3 / 100)",
		" Update state (0x101) committing, progress: 99.00 (990 / 1000)",
		" Update state (0x401) stopping, progress: 1.00 (1 / 100)",
		"[  0%] Checking for available updates...",
		"[ 45%] Downloading update (12,345 of 27,000 KB)...",
		"[100%] Download complete.",
		"[----] Extracting package...",
		"Connecting anonymously to Steam Public...OK",
		"Logging in user 'anonymous' to Steam Public...OK",
		"Waiting for user info...OK",
		"Waiting for client config...OK",
		"Loading Steam API...OK",
		"Success! App '896660' fully installed.",
	} {
		p, ok := ParseProgress(line)
		fmt.Fprintf(&out, "%q\t%v\t%s\t%g\n", line, ok, p.Step, p.Percent)
	}
	out.WriteString("== FriendlyError ==\n")
	for _, msg := range []string{
		"Failed to install app '896660' (Disk space)",
		"App '896660' state is 0x202 after update job.",
		"Failed to install app '896660' (Disk write failure)",
		"App '896660' state is 0x602 after update job.",
		"Timeout downloading item",
		"Failed to install app '896660' (No connection)",
		"Failed to connect",
		"steamcmd: Bad CPU type in executable",
		"App '896660' state is 0x6 after update job.",
		"Failed to install app '896660' (Invalid platform)",
		"something nobody has seen",
	} {
		fmt.Fprintf(&out, "%q\t%s\n", msg, FriendlyError(msg))
	}
	golden(t, "valheim_texts.golden", out.String())
}
