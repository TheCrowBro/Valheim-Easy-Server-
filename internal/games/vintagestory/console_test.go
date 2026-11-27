package vintagestory

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// TestConsoleLinesEndWithCRLF: every line the app types (the save line and
// the stop chain's console step) reaches the server ending with "\r\n",
// the app's own "\n" included, which is the only ending the Windows server
// takes, and is the plain command once trimmed, as the server trims it
// (FACTS.md, "The console").
func TestConsoleLinesEndWithCRLF(t *testing.T) {
	lines := map[string]string{"save line": saveLine}
	for _, st := range (&server{}).Stop(game.Record{}) {
		if st.Kind == "console" {
			lines["stop step"] = st.Line
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want the save line and a console stop step, got %q", lines)
	}
	for what, l := range lines {
		typed := l + "\n" // as the manager and gametest type it, in one write
		if !strings.HasSuffix(typed, "\r\n") {
			t.Errorf("the %s is typed as %q: the Windows server never runs a line that doesn't end with \"\\r\\n\"", what, typed)
		}
		if cmd := strings.TrimSpace(typed); cmd != "/autosavenow" && cmd != "/stop" {
			t.Errorf("the %s runs as %q", what, cmd)
		}
	}
}

// TestStandInConsoleLineEnd: the stand-in reads its console as the real
// server does. A line typed with "\n" alone runs on Linux and the Mac; on
// Windows it is lost and the console goes deaf, so the save line typed
// after it is lost too, and Ctrl+C (the stop after a takeover) is what
// still stops it. The app's own lines end with "\r\n" and run everywhere
// (TestConformance), so this is what keeps the stand-in honest about it.
func TestStandInConsoleLineEnd(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{Change: withPassword})
	s.WaitEvent("online", "", 30*time.Second)
	logPath := s.Plan().LogPath
	if err := s.Console("/autosavenow"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if !handled(t, logPath, 1, 15*time.Second) {
			t.Fatal(`a line typed with "\n" alone wasn't run, but "\n" is the line ending here`)
		}
		s.Stop()
		return
	}
	if handled(t, logPath, 1, 5*time.Second) {
		t.Fatal(`on Windows the stand-in ran a line typed with "\n" alone; the real server doesn't`)
	}
	if err := s.Console(saveLine); err != nil {
		t.Fatal(err)
	}
	if handled(t, logPath, 1, 5*time.Second) {
		t.Fatal(`on Windows the stand-in still reads its console after a line without "\r\n"; the real server stops reading`)
	}
	s.Takeover()
	s.Stop()
}

// handled says whether the server's log says it ran at least n console
// commands within the wait.
func handled(t *testing.T, logPath string, n int, within time.Duration) bool {
	t.Helper()
	for end := time.Now().Add(within); ; time.Sleep(100 * time.Millisecond) {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), "Handling Console Command") >= n {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}
