package terraria

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

// readLog reads one of the real server's logs in testdata/real, as the
// app's log reader gives it to the Monitor: line by line, without the line
// ends.
func readLog(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "real", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// feed runs lines through a new Monitor, one second apart, and returns it
// with the events and the lines shown on the Log page.
func feed(lines []string) (*monitor, []game.Event, []string) {
	m := newMonitor(nil)
	var evs []game.Event
	var shown []string
	now := time.Date(2026, 11, 26, 19, 0, 0, 0, time.UTC)
	for _, l := range lines {
		out := m.Line(l, now)
		evs = append(evs, out.Events...)
		if !out.Hide {
			shown = append(shown, out.Text)
		}
		now = now.Add(time.Second)
	}
	return m, evs, shown
}

// transcript is what the Monitor made of a log, as a golden file holds it.
func transcript(evs []game.Event, shown []string, st game.RunState) string {
	var b strings.Builder
	b.WriteString("# events\n")
	for _, e := range evs {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.Kind, e.Name, e.Text)
	}
	b.WriteString("# shown on the Log page\n")
	for _, l := range shown {
		b.WriteString(l + "\n")
	}
	var names []string
	for _, p := range st.Players {
		names = append(names, p.Name)
	}
	fmt.Fprintf(&b, "# state\nready=%v version=%s count=%d players=%s saved=%v stopping=%v\n",
		st.Ready, st.Version, st.Count, strings.Join(names, ","), !st.LastSave.IsZero(), st.Stopping)
	return b.String()
}

// TestMonitorGolden reads every real log in testdata/real and compares
// what the Monitor made of it with testdata/monitor/<log>.txt. Run with
// -update to rewrite those after a deliberate change, then read the diff.
func TestMonitorGolden(t *testing.T) {
	logs, err := filepath.Glob(filepath.Join("testdata", "real", "*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no real logs: %v", err)
	}
	for _, path := range logs {
		name := filepath.Base(path)
		m, evs, shown := feed(readLog(t, name))
		got := transcript(evs, shown, m.State())
		golden := filepath.Join("testdata", "monitor", strings.TrimSuffix(name, ".log")+".txt")
		if *update {
			os.MkdirAll(filepath.Dir(golden), 0o755)
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run the tests with -update to make it)", err)
		}
		if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
			t.Errorf("%s: the Monitor's reading changed.\n--- got\n%s\n--- want\n%s", name, got, want)
		}
	}
}

// kinds lists the events' kinds and names, "join:Alex".
func kinds(evs []game.Event) []string {
	var out []string
	for _, e := range evs {
		k := e.Kind
		if e.Name != "" {
			k += ":" + e.Name
		}
		out = append(out, k)
	}
	return out
}

// The checks below spell out, log by log, what matters most in the
// goldens above.

func TestMonitorNewWorld(t *testing.T) {
	m, evs, shown := feed(readLog(t, "new-world.log"))
	if got := kinds(evs); !slices.Equal(got, []string{"online", "saved"}) {
		t.Errorf("events %q, want online then saved (the first save)", got)
	}
	st := m.State()
	if !st.Ready || st.Version != "1.4.6.1" || st.Count != 0 || st.LastSave.IsZero() {
		t.Errorf("state %+v", st)
	}
	// The byte order marks and the prompt are gone; each progress step
	// shows once.
	for _, want := range []string{"Error Logging Enabled.", "Server started", "Resetting game objects 1%", "Saving world data: 3%",
		"Validating world save: 48%", "Backing up world file", "Listening on port 17777"} {
		if !slices.Contains(shown, want) {
			t.Errorf("the Log page doesn't show %q", want)
		}
	}
	for _, l := range shown {
		if strings.Contains(l, "\uFEFF") || strings.HasPrefix(l, ": ") || strings.HasPrefix(l, "Saving world data: 19%") {
			t.Errorf("the Log page shows %q", l)
		}
	}
}

func TestMonitorJoinLeaveSave(t *testing.T) {
	// It starts with the end of a client with another version, and
	// clients the server turned away for their names.
	m, evs, _ := feed(readLog(t, "join-leave-save.log"))
	if got := kinds(evs); !slices.Equal(got, []string{"bad_version", "join:Alex", "leave:Alex", "saved"}) {
		t.Fatalf("events %q", got)
	}
	if evs[1].Text != "Alex joined the game." || evs[2].Text != "Alex left the game." {
		t.Errorf("texts %q, %q", evs[1].Text, evs[2].Text)
	}
	if st := m.State(); st.Count != 0 || len(st.Players) != 0 || st.LastSave.IsZero() {
		t.Errorf("state %+v", st)
	}
}

func TestMonitorChatAndTwoPlayers(t *testing.T) {
	m, evs, shown := feed(readLog(t, "chat-two-players-exit.log"))
	// "<Big Bob> Alex has joined." is chat, not a join.
	want := []string{"join:Big Bob", "leave:Big Bob", "saved", "join:Alex", "leave:Alex", "saved",
		"join:Watcher", "join:Alex", "leave:Alex", "leave:Watcher", "saved", "stopping"}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Errorf("events\n %q\nwant\n %q", got, want)
	}
	if !slices.Contains(shown, "<Big Bob> Alex has joined.") {
		t.Error("the chat line isn't shown")
	}
	st := m.State()
	if !st.Stopping || st.Count != 0 {
		t.Errorf("state %+v", st)
	}
	// The exit's save prints nothing; the run's end counts it.
	last := st.LastSave
	m.End(last.Add(time.Minute))
	if !m.State().LastSave.After(last) {
		t.Error("End after \"Saving before exit...\" didn't count the save")
	}
}

func TestMonitorTurnedAway(t *testing.T) {
	_, evs, _ := feed(readLog(t, "password-full-exit.log"))
	want := []string{"online", "wrong_password", "join:Alex", "server_full", "leave:Alex", "saved", "join:Alex", "stopping"}
	if got := kinds(evs); !slices.Equal(got, want) {
		t.Errorf("events\n %q\nwant\n %q", got, want)
	}
	_, evs, _ = feed(readLog(t, "console-and-version-mismatch.log"))
	n := 0
	for _, e := range evs {
		if e.Kind == "bad_version" {
			n++
			if !strings.Contains(e.Text, "Terraria 1.4.6.1") {
				t.Errorf("bad_version text %q doesn't say the server's version", e.Text)
			}
		}
	}
	if n != 3 {
		t.Errorf("%d bad_version events, want 3: %q", n, kinds(evs))
	}
}

func TestMonitorEndLeavesEveryone(t *testing.T) {
	m, _, _ := feed(readLog(t, "password-full-exit.log"))
	if st := m.State(); st.Count != 1 || st.Players[0].Name != "Alex" {
		t.Fatalf("before the end: %+v", st)
	}
	evs := m.End(time.Now())
	if got := kinds(evs); !slices.Equal(got, []string{"leave:Alex"}) {
		t.Errorf("End gave %q", got)
	}
	if st := m.State(); st.Ready || st.Count != 0 {
		t.Errorf("after the end: %+v", st)
	}
}

func TestMonitorDamagedWorld(t *testing.T) {
	_, evs, _ := feed(readLog(t, "damaged-world.log"))
	if got := kinds(evs); !slices.Equal(got, []string{"load_failed"}) {
		t.Errorf("events %q", got)
	}
}

func TestMonitorAutosave(t *testing.T) {
	_, evs, _ := feed(readLog(t, "autosave-10min.log"))
	if got := kinds(evs); !slices.Equal(got, []string{"online", "join:Stayer", "saved"}) {
		t.Errorf("events %q", got)
	}
}

// A takeover reads the whole log again with a new Monitor: it ends where
// the first one did.
func TestMonitorReplay(t *testing.T) {
	lines := append(readLog(t, "load-world.log"), readLog(t, "join-leave-save.log")...)
	first, _, _ := feed(lines)
	again, _, _ := feed(lines)
	a, b := first.State(), again.State()
	if a.Ready != b.Ready || a.Count != b.Count || a.Version != b.Version {
		t.Errorf("replay state %+v, first %+v", b, a)
	}
}

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"\uFEFF\uFEFF\uFEFF\uFEFFError Logging Enabled.": "Error Logging Enabled.",
		": Server started":      "Server started",
		": : Port: 7777":        "Port: 7777",
		": ":                    "",
		":":                     "",
		"Alex has joined.  \r":  "Alex has joined.",
		"<Alex> : not a prompt": "<Alex> : not a prompt",
	} {
		if got := cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute:                    "1 minute",
		45 * time.Minute:               "45 minutes",
		time.Hour:                      "1 hour",
		2*time.Hour + 5*time.Minute:    "2 hours 5 minutes",
		time.Hour + time.Minute + 20e9: "1 hour 1 minute",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
