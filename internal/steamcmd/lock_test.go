package steamcmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/steamcmd/steamcmdtest"
)

// steps collects the progress texts an install showed, safe to read while
// the install runs.
type steps struct {
	mu   sync.Mutex
	list []string
}

func (s *steps) add(p Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, p.Step)
}

func (s *steps) has(step string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.list {
		if x == step {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// background runs an install in a goroutine. The test's cleanup cancels it
// and waits, so the app-wide lock is free for the next test.
func background(t *testing.T, s *SteamCMD, ctx context.Context, progress func(Progress)) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancel = context.WithCancel(ctx)
	ch := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ch <- s.Install(ctx, false, progress)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("an install didn't stop when cancelled")
		}
	})
	return cancel, ch
}

func result(t *testing.T, done <-chan error, limit time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatal("the install didn't finish in time")
		return nil
	}
}

// Another game's app id goes to SteamCMD, its success line is the one that
// counts, and its name and size are in the texts.
func TestOtherApp(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	steamcmdtest.Put(t, steamDir, ` Update state (0x61) downloading, progress: 40.00 (40 / 100)
@success
`)
	server := filepath.Join(dir, "server")
	s := &SteamCMD{Dir: steamDir, InstallDir: server, AppID: "1169370", Name: "Necesse", SizeGB: 0.5}
	var got steps
	if err := s.Install(context.Background(), true, got.add); err != nil {
		t.Fatal(err)
	}
	if !got.has("Downloading the Necesse server…") || !got.has("Server files are up to date.") {
		t.Errorf("progress = %q", got.list)
	}
	s.Branch = "unstable"
	if err := s.Install(context.Background(), false, got.add); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"+force_install_dir", server, "+login", "anonymous", "+app_update", "1169370", "validate", "+quit"},
		{"+force_install_dir", server, "+login", "anonymous", "+app_update", "1169370", "-beta", "unstable", "+quit"},
	}
	if runs := steamcmdtest.Args(t, steamDir); !reflect.DeepEqual(runs, want) {
		t.Errorf("SteamCMD was run with\n%q\nwant\n%q", runs, want)
	}

	// Valheim's success line doesn't finish Necesse's install.
	steamcmdtest.SetScript(t, steamDir, "Success! App '896660' fully installed.\n")
	err := s.Install(context.Background(), false, got.add)
	if err == nil || !strings.Contains(err.Error(), "SteamCMD finished without confirming the install") {
		t.Errorf("Install with another app's success line = %v", err)
	}

	// Disk space texts name the game and its size.
	steamcmdtest.SetScript(t, steamDir, "ERROR! Failed to install app '2394010' (Disk space)\n")
	p := &SteamCMD{Dir: steamDir, InstallDir: server, AppID: "2394010", Name: "Palworld", SizeGB: 6}
	err = p.Install(context.Background(), false, got.add)
	if want := "There isn't enough free disk space for the Palworld server (it needs about 6 GB)."; err == nil || err.Error() != want {
		t.Errorf("Install = %v, want %q", err, want)
	}
	if got := friendlyError("Disk space", "Satisfactory", 15.5); got != "There isn't enough free disk space for the Satisfactory server (it needs about 15.5 GB)." {
		t.Errorf("friendlyError = %q", got)
	}
}

func TestInstalledBuildFor(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "steamapps"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "\"AppState\"\n{\n\t\"appid\"\t\t\"1169370\"\n\t\"buildid\"\t\t\"16371234\"\n\t\"LastUpdated\"\t\t\"1764000000\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "steamapps", "appmanifest_1169370.acf"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	id, when, ok := InstalledBuildFor(dir, "1169370")
	if !ok || id != "16371234" || !when.Equal(time.Unix(1764000000, 0)) {
		t.Errorf("InstalledBuildFor = %q %v %v", id, when, ok)
	}
	if _, _, ok := InstalledBuild(dir); ok {
		t.Error("Valheim's build was found in another app's folder")
	}
	if _, _, ok := InstalledBuildFor(dir, "../1169370"); ok {
		t.Error("an app id that isn't a number was read")
	}
}

// An app id or branch that isn't plain would be read by SteamCMD as more
// commands, so it never gets that far.
func TestBadAppIDOrBranch(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	steamcmdtest.Put(t, steamDir, "@success\n")
	for _, s := range []*SteamCMD{
		{Dir: steamDir, InstallDir: dir, AppID: "123 +quit"},
		{Dir: steamDir, InstallDir: dir, AppID: "1169370", Branch: "x +app_update 1"},
	} {
		if err := s.Install(context.Background(), false, func(Progress) {}); err == nil {
			t.Errorf("Install(%q, %q) worked", s.AppID, s.Branch)
		}
	}
	if runs := steamcmdtest.Args(t, steamDir); len(runs) != 0 {
		t.Errorf("SteamCMD ran: %q", runs)
	}
}

// The acceptance test of spec 2.3: while another game's 10-minute install
// holds SteamCMD, a start's update skips at once (well under 30 s) with the
// text the manager puts in its warning, and runs nothing; an install waits
// for its turn and then runs.
func TestStartUpdateSkipsWhileAnotherInstalls(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	steamcmdtest.Put(t, steamDir, "@success\n")
	steamcmdtest.SetScriptFor(t, steamDir, 1, ` Update state (0x61) downloading, progress: 1.00 (1 / 100)
@touch downloading
@sleep 10m
@success
`)
	big := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "satisfactory"), AppID: "1690800", Name: "Satisfactory", SizeGB: 15.5}
	cancelBig, bigDone := background(t, big, context.Background(), func(Progress) {})
	steamcmdtest.Await(t, steamDir, "downloading", 20*time.Second)

	valheim := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "valheim")}
	var shown steps
	start := time.Now()
	err := valheim.Install(SkipIfBusy(context.Background()), false, shown.add)
	if took := time.Since(start); took >= 30*time.Second {
		t.Fatalf("the start's update took %v", took)
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Install = %v, want ErrBusy", err)
	}
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Name != "Satisfactory" {
		t.Fatalf("Install = %#v", err)
	}
	// The manager's existing warning (server.go) wraps the error as is.
	warning := "Couldn't check for server updates (" + err.Error() + "). Starting the version that's installed."
	if want := "Couldn't check for server updates (Satisfactory is downloading). Starting the version that's installed."; warning != want {
		t.Errorf("warning = %q, want %q", warning, want)
	}
	if len(shown.list) != 0 {
		t.Errorf("the skipped update showed %q", shown.list)
	}
	if runs := steamcmdtest.Args(t, steamDir); len(runs) != 1 {
		t.Fatalf("SteamCMD ran %d times, want only the big install", len(runs))
	}

	// An install (not a start) waits, saying for what, and runs nothing yet.
	var waiting steps
	_, valheimDone := background(t, valheim, context.Background(), waiting.add)
	waitFor(t, "the waiting text", func() bool { return waiting.has("Waiting for the Satisfactory download to finish…") })
	time.Sleep(200 * time.Millisecond)
	if runs := steamcmdtest.Args(t, steamDir); len(runs) != 1 {
		t.Fatalf("a second SteamCMD ran while the first one was downloading")
	}

	// When the big install ends (here: cancelled), the waiting one runs.
	cancelBig()
	if err := result(t, bigDone, 20*time.Second); err == nil || err.Error() != "cancelled" {
		t.Errorf("the big install = %v", err)
	}
	if err := result(t, valheimDone, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	runs := steamcmdtest.Args(t, steamDir)
	if len(runs) != 2 || runs[1][5] != AppID {
		t.Errorf("runs = %q", runs)
	}

	// With SteamCMD free, a start's update runs as before.
	if err := valheim.Install(SkipIfBusy(context.Background()), false, func(Progress) {}); err != nil {
		t.Errorf("a start's update with SteamCMD free = %v", err)
	}
}

// Cancelling an install that is waiting for its turn ends it at once.
func TestWaitingInstallCancels(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	steamcmdtest.Put(t, steamDir, "@touch running\n@sleep 10m\n")
	first := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "a"), AppID: "380870", Name: "Project Zomboid"}
	background(t, first, context.Background(), func(Progress) {})
	steamcmdtest.Await(t, steamDir, "running", 20*time.Second)

	second := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "b"), AppID: "1963720", Name: "Core Keeper"}
	var shown steps
	cancel, done := background(t, second, context.Background(), shown.add)
	waitFor(t, "the waiting text", func() bool { return shown.has("Waiting for the Project Zomboid download to finish…") })
	cancel()
	if err := result(t, done, 5*time.Second); err == nil || err.Error() != "cancelled" {
		t.Errorf("Install = %v", err)
	}
}

// The lock covers SteamCMD's own first download and unpacking into the
// shared folder: a second game waits instead of downloading it again over
// the first one's half-written files.
func TestLockCoversSteamCMDDownload(t *testing.T) {
	dir := t.TempDir()
	steamDir := filepath.Join(dir, "steamcmd")
	if err := os.MkdirAll(steamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	steamcmdtest.SetScript(t, steamDir, "@success\n")

	archive, name := steamCMDArchive(t)
	var requests atomic.Int32
	hit := make(chan struct{}, 10)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		hit <- struct{}{}
		<-release
		w.Write(archive)
	}))
	defer srv.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	old := downloads[runtime.GOOS]
	downloads[runtime.GOOS] = srv.URL + "/" + name
	defer func() { downloads[runtime.GOOS] = old }()

	necesse := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "necesse"), AppID: "1169370", Name: "Necesse"}
	_, necesseDone := background(t, necesse, context.Background(), func(Progress) {})
	select {
	case <-hit:
	case <-time.After(20 * time.Second):
		t.Fatal("SteamCMD's download never started")
	}

	valheim := &SteamCMD{Dir: steamDir, InstallDir: filepath.Join(dir, "valheim")}
	if err := valheim.Install(SkipIfBusy(context.Background()), false, func(Progress) {}); !errors.Is(err, ErrBusy) || err.Error() != "Necesse is downloading" {
		t.Errorf("a start's update during SteamCMD's download = %v", err)
	}
	var shown steps
	_, valheimDone := background(t, valheim, context.Background(), shown.add)
	waitFor(t, "the waiting text", func() bool { return shown.has("Waiting for the Necesse download to finish…") })
	time.Sleep(200 * time.Millisecond)
	if n := requests.Load(); n != 1 {
		t.Fatalf("SteamCMD was downloaded %d times while the first download ran", n)
	}

	releaseOnce.Do(func() { close(release) })
	if err := result(t, necesseDone, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := result(t, valheimDone, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("SteamCMD was downloaded %d times", n)
	}
	if runs := steamcmdtest.Args(t, steamDir); len(runs) != 2 {
		t.Errorf("runs = %q", runs)
	}
}

// steamCMDArchive packs the stand-in SteamCMD the way Valve ships SteamCMD
// for this system: steamcmd.exe in a zip on Windows, steamcmd.sh in a
// .tar.gz elsewhere.
func steamCMDArchive(t *testing.T) (data []byte, name string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "self")
	if err := steamcmdtest.CopySelf(exe); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("steamcmd.exe")
		if err != nil {
			t.Fatal(err)
		}
		w.Write(self)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), "steamcmd.zip"
	}
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "steamcmd.sh", Mode: 0o755, Size: int64(len(self)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(self)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), "steamcmd_" + runtime.GOOS + ".tar.gz"
}
