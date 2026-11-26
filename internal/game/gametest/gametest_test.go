package gametest

import (
	"errors"
	"slices"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest/demo"
	"valheim-easy-server/internal/rcon"
)

// TestConformanceDemo runs Conformance on each kind of Demo Game, which
// between them stop in every way a chain can: Ctrl+C alone, a console line
// then Ctrl+C, an API call then Ctrl+C (with a probe), and an RCON command
// then Ctrl+C. They run at once, each with its own stand-in, ports and
// script.
func TestConformanceDemo(t *testing.T) {
	for _, g := range []demo.Game{
		{ID: "demo"},
		{ID: "democonsole", Console: true},
		{ID: "demoapi", API: true},
		{ID: "demorcon", RCON: true},
	} {
		t.Run(g.Info().ID, func(t *testing.T) {
			if g.RCON && !rconWritten() {
				t.Skip("internal/rcon isn't written yet (stream RC); this kind runs once it is")
			}
			t.Parallel()
			Conformance(t, g, Options{Fake: BuildFakeserver(t)})
		})
	}
}

// rconWritten says whether internal/rcon is more than its phase 0 stub.
func rconWritten() bool {
	s, err := rcon.NewTestServer("127.0.0.1:0", "x", func(string) string { return "" })
	if err != nil {
		return !errors.Is(err, errors.ErrUnsupported)
	}
	s.Close()
	return true
}

// TestConformanceReal runs Conformance with Options.Real: the adapter's
// own installer and plan, no fakeserver.json, as a realserver-tagged test
// runs a real server. Demo Game's "real" server is the stand-in, copied
// alone, so it runs with the stand-in's defaults (its clocks at normal
// speed, no script).
func TestConformanceReal(t *testing.T) {
	Conformance(t, demo.Game{ID: "demoreal", Console: true, Real: BuildFakeserver(t)}, Options{Real: true})
}

// TestConsoleStop: with the console the app holds, the console line alone
// stops a console game; after a takeover there is no console, so that step
// gives ErrNoConsole and Ctrl+C stops it instead.
func TestConsoleStop(t *testing.T) {
	s := Start(t, demo.Game{ID: "democonsole", Console: true}, Options{})
	s.WaitEvent("online", "", 30*time.Second)
	if saved := s.Stop(); saved.IsZero() {
		t.Error("the console stop didn't save the world")
	}
	if len(s.stopLog) != 1 || s.stopLog[0].Kind != "console" || s.stopLog[0].Err != nil {
		t.Fatalf("stop steps with a console = %+v, want just the console line", s.stopLog)
	}

	s.StartAgain()
	s.WaitEvent("online", "", 30*time.Second)
	s.Takeover()
	if saved := s.Stop(); saved.IsZero() {
		t.Error("Ctrl+C after the takeover didn't save the world")
	}
	if len(s.stopLog) != 2 || !errors.Is(s.stopLog[0].Err, game.ErrNoConsole) || s.stopLog[1].Kind != "interrupt" || s.stopLog[1].Err != nil {
		t.Fatalf("stop steps after a takeover = %+v, want the console step's ErrNoConsole then Ctrl+C", s.stopLog)
	}
}

// TestTwoAtOnce runs two stand-ins at the same time, each with its own
// script: each sees only its own players, on its own ports.
func TestTwoAtOnce(t *testing.T) {
	fake := BuildFakeserver(t)
	a := Start(t, demo.Game{ID: "demoa"}, Options{Fake: fake, Script: "join:Ana@1,death:Ana@2,leave:Ana@3"})
	b := Start(t, demo.Game{ID: "demob", API: true}, Options{Fake: fake, Script: "join:Bo@1.5,join:Cy@2,leave:Bo@3"})
	if pa, pb := a.store.Get().Ports["game"], b.store.Get().Ports["game"]; pa == pb {
		t.Fatalf("both got port %d", pa)
	}
	for _, s := range []*Session{a, b} {
		s.WaitEvent("online", "", 30*time.Second)
	}
	a.WaitEvent("join", "Ana", 15*time.Second)
	a.WaitEvent("death", "Ana", 15*time.Second)
	b.WaitEvent("join", "Bo", 15*time.Second)
	b.WaitEvent("join", "Cy", 15*time.Second)
	a.WaitEvent("leave", "Ana", 15*time.Second)
	b.WaitEvent("leave", "Bo", 15*time.Second)
	if got := names(b.State()); !slices.Equal(got, []string{"Cy"}) {
		t.Errorf("b's players = %q, want [Cy]", got)
	}
	for _, c := range []struct {
		s    *Session
		want []string
	}{{a, []string{"Ana"}}, {b, []string{"Bo", "Cy"}}} {
		c.s.mu.Lock()
		var seen []string
		for _, e := range c.s.run.events {
			if e.Kind == "join" && !slices.Contains(seen, e.Name) {
				seen = append(seen, e.Name)
			}
		}
		c.s.mu.Unlock()
		slices.Sort(seen)
		if !slices.Equal(seen, c.want) {
			t.Errorf("%s saw joins of %q, want %q", c.s.info.ID, seen, c.want)
		}
	}
	a.Stop()
	b.Stop()
}

func names(st game.RunState) []string {
	var out []string
	for _, p := range st.Players {
		out = append(out, p.Name)
	}
	return out
}
