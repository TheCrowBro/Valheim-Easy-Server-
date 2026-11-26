package main

// The names and flags that copies of the app already installed depend on
// (docs/dev/multigame/spec.md, 1.2 and I5). An update replaces only the
// program, and the program it replaces comes back if the new one doesn't
// start, so these never change, even when the name people see does. Each
// test compares a value with its literal: renaming one fails here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/web"
)

// runMainEnv makes this test program run the app's main instead of the
// tests (see init), so TestFlagsUnchanged sees the flags exactly as the
// app sets them up: they're made inside main.
const runMainEnv = "VES_TEST_RUN_MAIN"

func init() {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
}

func TestIdentity(t *testing.T) {
	for _, c := range []struct{ what, got, want string }{
		{"data folder (Mac, and the local app data fallback)", dataFolderName, "ValheimEasyServer"},
		{"data folder next to the program in Downloads or on the Desktop", legacyFolderName, "Valheim Easy Server"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q: installed copies keep their files there", c.what, c.got, c.want)
		}
	}
}

// The data folder is found where installed copies keep it.
func TestDataFolderUnchanged(t *testing.T) {
	base := t.TempDir()
	// Somewhere else for the temp folder, so the folders below don't count
	// as being in it (a program run from the temp folder keeps its files
	// elsewhere).
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, filepath.Join(base, "elsewhere"))
	}
	cfg := filepath.Join(base, "config")
	for _, name := range []string{"XDG_CONFIG_HOME", "AppData", "LOCALAPPDATA"} {
		t.Setenv(name, cfg)
	}
	if runtime.GOOS == "darwin" {
		// A Mac app always keeps its files in Application Support.
		t.Setenv("HOME", base)
		got, err := resolveDataDir("", filepath.Join(base, "Applications", "Valheim Easy Server.app", "Contents", "MacOS", "ValheimEasyServer"))
		if want := filepath.Join(base, "Library", "Application Support", "ValheimEasyServer"); err != nil || got != want {
			t.Fatalf("Mac data folder = %q (%v), want %q", got, err, want)
		}
		return
	}
	for _, folder := range []string{"Downloads", "Desktop", "desktop"} {
		dir := filepath.Join(base, folder)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := resolveDataDir("", filepath.Join(dir, "ValheimEasyServer.exe"))
		if want := filepath.Join(dir, "Valheim Easy Server"); err != nil || got != want {
			t.Errorf("run from %s: data folder = %q (%v), want %q", folder, got, err, want)
		}
	}
	// A program in a folder of its own keeps its files there.
	own := filepath.Join(base, "Valheim Server")
	os.MkdirAll(own, 0o755)
	if got, err := resolveDataDir("", filepath.Join(own, "ValheimEasyServer.exe")); err != nil || got != own {
		t.Errorf("run from its own folder: data folder = %q (%v), want %q", got, err, own)
	}
	// Run from the temp folder (straight out of a zip), it uses the user's
	// local app data.
	temp := filepath.Join(base, "elsewhere", "Temp1_ValheimEasyServer-windows.zip")
	os.MkdirAll(temp, 0o755)
	if got, err := resolveDataDir("", filepath.Join(temp, "ValheimEasyServer.exe")); err != nil || got != filepath.Join(cfg, "ValheimEasyServer") {
		t.Errorf("run from the temp folder: data folder = %q (%v), want %q", got, err, filepath.Join(cfg, "ValheimEasyServer"))
	}
}

// .instance.json tells a second start of the app, and the copy that just
// updated it, which port the control panel is on.
func TestInstanceFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	srv := web.New(web.Options{Logger: log.New(io.Discard, "", 0)})
	if err := srv.Listen(0); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	writeInstance(dir, srv)
	b, err := os.ReadFile(filepath.Join(dir, ".instance.json"))
	if want := fmt.Sprintf(`{"port":%d}`, srv.Port()); err != nil || string(b) != want {
		t.Fatalf(".instance.json = %q (%v), want %q", b, err, want)
	}
}

// runMain runs the app's main with args, in this test program, and returns
// what it printed. It must end with exit code want.
func runMain(t *testing.T, want int, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	// Should main ever not stop at -h, it starts on a folder of its own
	// with no window (and is stopped by the timeout).
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running main %q: %v", args, err)
	}
	if code != want {
		t.Fatalf("main %q exited with %d, want %d:\n%s", args, code, want, out)
	}
	return string(out)
}

// TestFlagsUnchanged: an update starts the new program with the old one's
// flags (relaunchArgs), and a rollback starts the old program with the new
// one's, so every flag stays, and none is ever added: an older build exits
// with code 2 on a flag it doesn't know. Developer switches are EGS_DEV_*
// environment variables instead.
func TestFlagsUnchanged(t *testing.T) {
	// -psn_… (added by older macOS versions) is accepted: -h then ends it
	// with the list of flags.
	usage := runMain(t, 0, "-psn_0_1234567", "-h")
	got := map[string]string{}
	var port string // what -h says about -port
	last := ""      // the flag the lines below describe
	for _, line := range strings.Split(usage, "\n") {
		if m := regexp.MustCompile(`^  -(\S+)(?: (\S+))?$`).FindStringSubmatch(line); m != nil {
			kind := m[2]
			if kind == "" {
				kind = "bool"
			}
			got[m[1]], last = kind, m[1]
		} else if last == "port" {
			port += line
		}
	}
	want := map[string]string{
		"data":             "string",
		"background":       "bool",
		"no-window":        "bool",
		"port":             "int",
		"dev-fake-server":  "string",
		"dev-fake-discord": "string",
		"dev-fake-github":  "string",
		"detached":         "bool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flags = %v, want %v\n%s", sortedKeys(got), sortedKeys(want), usage)
	}
	if !strings.Contains(port, "(default 8456)") {
		t.Errorf("the control panel's port isn't 8456 by default: %q", port)
	}
	// A flag it doesn't know: exit code 2, which an older build gives too.
	runMain(t, 2, "--no-such-flag")

	for _, c := range []struct {
		in   []string
		want []string
	}{
		// As the update and rollback tests in CI start it.
		{[]string{"--no-window", "--data", "D", "--port", "18457", "--dev-fake-server", "F", "--dev-fake-github", "http://127.0.0.1:18500", "--dev-fake-discord", "http://127.0.0.1:18501"},
			[]string{"--no-window", "--data", "D", "--dev-fake-server", "F", "--dev-fake-github", "http://127.0.0.1:18500", "--dev-fake-discord", "http://127.0.0.1:18501", "--port=8456"}},
		// Opened from Finder: the copy that carries on in the background.
		{[]string{"-psn_0_1234567", "--detached"}, []string{"-psn_0_1234567", "--detached", "--background", "--port=8456"}},
		// Started at sign-in.
		{[]string{"--background"}, []string{"--background", "--port=8456"}},
		{[]string{"--data=D", "--port=9000", "-background=false"}, []string{"--data=D", "-background=false", "--background", "--port=8456"}},
	} {
		if got := relaunchArgs(c.in, 8456); !reflect.DeepEqual(got, c.want) {
			t.Errorf("relaunchArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The program's identity outside the code: the Mac app's bundle and the
// Windows program's details. macOS keys the login item, permissions and
// the firewall's list to the bundle, and Windows' firewall, Task Manager
// and startup list show the program's details.
func TestProgramIdentityUnchanged(t *testing.T) {
	plist, err := os.ReadFile(filepath.Join("macapp", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"CFBundleIdentifier": "com.thecrowbro.valheimeasyserver",
		"CFBundleExecutable": "ValheimEasyServer",
	} {
		m := regexp.MustCompile(`<key>` + key + `</key>\s*<string>([^<]*)</string>`).FindSubmatch(plist)
		if m == nil || string(m[1]) != want {
			t.Errorf("Info.plist %s = %q, want %q", key, m, want)
		}
	}

	b, err := os.ReadFile(filepath.Join("winres", "winres.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Manifest map[string]map[string]struct {
			Identity struct {
				Name string `json:"name"`
			} `json:"identity"`
		} `json:"RT_MANIFEST"`
		Version map[string]map[string]struct {
			Info map[string]map[string]string `json:"info"`
		} `json:"RT_VERSION"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if got := res.Manifest["#1"]["0409"].Identity.Name; got != "ValheimEasyServer" {
		t.Errorf("winres.json manifest identity name = %q, want ValheimEasyServer", got)
	}
	info := res.Version["#1"]["0000"].Info["0409"]
	if info["InternalName"] != "ValheimEasyServer" || info["OriginalFilename"] != "ValheimEasyServer.exe" {
		t.Errorf("winres.json InternalName = %q, OriginalFilename = %q, want ValheimEasyServer and ValheimEasyServer.exe",
			info["InternalName"], info["OriginalFilename"])
	}

	// The build number is this workflow's run number, which starts again
	// at 1 if the file is renamed: installed copies would never update
	// again. The release assets and the Mac bundle are made there.
	wf, err := os.ReadFile(filepath.Join(".github", "workflows", "build.yml"))
	if err != nil {
		t.Fatalf("the build workflow moved: %v", err)
	}
	for _, s := range []string{
		"Valheim Easy Server.app", "Contents/MacOS/ValheimEasyServer",
		"internal/jetkvm/dist/valheim-waker-armv7.gz",
	} {
		if !strings.Contains(string(wf), s) {
			t.Errorf("build.yml no longer has %q", s)
		}
	}
	for _, p := range releaseJobProblems(string(wf)) {
		t.Errorf("build.yml: %s", p)
	}
}

// releaseFiles are the files every release has, in the order build.yml
// lists them. Installed copies update only from a build-N release that has
// their program and its checksum (selfupdate.Latest), and the README sends
// people to the zips.
var releaseFiles = []string{
	"ValheimEasyServer-windows.zip", "ValheimEasyServer.exe", "ValheimEasyServer.exe.sha256",
	"ValheimEasyServer-mac.zip", "ValheimEasyServer-mac", "ValheimEasyServer-mac.sha256",
}

// releaseJob is build.yml's release job: from its "  release:" line to the
// next job, or the end of the file.
func releaseJob(wf string) string {
	wf = strings.ReplaceAll(wf, "\r\n", "\n") // as Git may check it out on Windows
	i := strings.Index(wf, "\n  release:\n")
	if i < 0 {
		return ""
	}
	job := wf[i+1:]
	if next := regexp.MustCompile(`\n  [A-Za-z0-9_-]+:[ \t]*\n`).FindStringIndex(job); next != nil {
		job = job[:next[0]+1]
	}
	return job
}

// releaseJobProblems lists what in build.yml's release job is no longer
// what installed copies need: the tag, the build number it carries, the
// checksum files and the published file lists. The same names appear in
// other jobs too, so these are checked in the release job itself, which
// is what publishes them. The rest of the job, and the other jobs, may
// change (the multi-game CI adds steps).
func releaseJobProblems(wf string) []string {
	job := releaseJob(wf)
	if job == "" {
		return []string{`the release job ("  release:") is gone`}
	}
	var out []string
	lines := map[string]bool{}
	for _, l := range strings.Split(job, "\n") {
		lines[strings.Join(strings.Fields(l), " ")] = true
	}
	for _, l := range []string{
		// The run number, as the build-N tag installed copies look for.
		"RUN_NUMBER: ${{ github.run_number }}",
		`tag="build-$RUN_NUMBER"`,
		// Each checksum file is the sum of its own program.
		"sha256sum ValheimEasyServer.exe > ValheimEasyServer.exe.sha256",
		"sha256sum ValheimEasyServer-mac > ValheimEasyServer-mac.sha256",
	} {
		if !lines[l] {
			out = append(out, "the release job has no line "+l)
		}
	}

	// A build's release is published from $files under $tag.
	want := strings.Join(releaseFiles, " ")
	files := regexp.MustCompile(`(?m)^\s*files="([^"]*)"\s*$`).FindAllStringSubmatch(job, -1)
	if len(files) != 1 {
		out = append(out, fmt.Sprintf(`the release job has %d files="…" lines, want one`, len(files)))
	} else if got := strings.Join(strings.Fields(files[0][1]), " "); got != want {
		out = append(out, fmt.Sprintf(`the release job publishes files="%s", want files="%s"`, got, want))
	}
	for _, cmd := range []string{`gh release create "$tag" $files `, `gh release upload "$tag" $files `} {
		if !strings.Contains(job, cmd) {
			out = append(out, "the release job no longer runs "+strings.TrimSpace(cmd))
		}
	}

	// A version tag's release names the same files itself.
	version := regexp.MustCompile(`gh release create "\$TAG" ([^\n]*)`).FindAllStringSubmatch(job, -1)
	if len(version) != 1 {
		out = append(out, fmt.Sprintf(`the release job has %d gh release create "$TAG" lines, want one`, len(version)))
	} else {
		var got []string
		for _, f := range strings.Fields(version[0][1]) {
			if strings.HasPrefix(f, "--") {
				break
			}
			got = append(got, f)
		}
		if strings.Join(got, " ") != want {
			out = append(out, fmt.Sprintf(`a version's release publishes %q, want %q`, strings.Join(got, " "), want))
		}
	}
	return out
}

// TestReleaseJobCheck: releaseJobProblems notices a change made only in the
// release job, where the same name elsewhere in build.yml would hide it from
// a search of the whole file.
func TestReleaseJobCheck(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(".github", "workflows", "build.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := strings.ReplaceAll(string(b), "\r\n", "\n")
	job := releaseJob(wf)
	if job == "" {
		t.Fatal("build.yml has no release job")
	}
	for _, c := range []struct{ what, old, new string }{
		{"the Windows checksum renamed", "ValheimEasyServer.exe.sha256", "EasyGameServer.exe.sha256"},
		{"the Windows program renamed", "ValheimEasyServer.exe ", "EasyGameServer.exe "},
		{"the Mac program renamed", "ValheimEasyServer-mac ", "EasyGameServer-mac "},
		{"the Mac checksum left out", " ValheimEasyServer-mac.sha256", ""},
		{"the tag renamed", `tag="build-$RUN_NUMBER"`, `tag="egs-$RUN_NUMBER"`},
		{"the build number changed", "${{ github.run_number }}", "${{ github.run_id }}"},
		{"the checksums swapped", "sha256sum ValheimEasyServer-mac >", "sha256sum ValheimEasyServer.exe >"},
	} {
		changed := strings.ReplaceAll(job, c.old, c.new)
		if changed == job {
			t.Errorf("%s: the release job has no %q to change", c.what, c.old)
			continue
		}
		if len(releaseJobProblems(strings.Replace(wf, job, changed, 1))) == 0 {
			t.Errorf("%s only in the release job: not noticed", c.what)
		}
	}
	// The rest may change: a step added to the release job (the multi-game
	// CI adds one), or a job after it that publishes something else.
	for what, changed := range map[string]string{
		"a step added to the release job": strings.Replace(wf, job, strings.Replace(job, "    steps:\n",
			"    steps:\n      - name: Check\n        run: gh run list --workflow real-server.yml\n", 1), 1),
		"a job after the release job": strings.TrimRight(wf, "\n") +
			"\n  extra:\n    runs-on: ubuntu-latest\n    steps:\n      - run: gh release create \"$TAG\" other.zip --repo x\n",
	} {
		if changed == wf {
			t.Errorf("%s: build.yml didn't change", what)
		} else if p := releaseJobProblems(changed); len(p) > 0 {
			t.Errorf("%s: %q", what, p)
		}
	}
}
