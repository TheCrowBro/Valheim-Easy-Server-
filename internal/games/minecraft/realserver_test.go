//go:build realserver

package minecraft

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// TestRealServer is Minecraft's real-server check (spec 5.2 and 7.4), run
// by real-server.yml on Windows and the Intel Mac (realserver.json): it
// downloads Mojang's newest release and the Java it needs, and runs them
// as the app does, without the manager.
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 45m ./internal/games/minecraft/
//
// It also runs on Linux, where the agents work (Linux isn't a platform the
// app offers Minecraft on, but the server and Temurin both run there).
func TestRealServer(t *testing.T) {
	t.Run("allow list, probe, join and stop", func(t *testing.T) {
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Real", Change: agreed})
		srv := s.Server()
		var err error
		if b, ok := srv.Build(); !ok || b.Version == "" {
			t.Fatalf("Build() after the install = %+v, %v", b, ok)
		}
		s.WaitEvent("online", "", 8*time.Minute)

		// "auto" with no sign of carrier-grade NAT: the list was on from
		// the first start (the server rewrites server.properties as it
		// starts, keeping what the plan set).
		props := parseProperties(readFile(t, filepath.Join(s.Env().Dirs.Server, "server.properties")))
		for _, k := range []string{"white-list", "enforce-whitelist", "enable-rcon", "online-mode"} {
			if v, _ := props.Get(k); v != "true" {
				t.Errorf("server.properties %s = %q, want true", k, v)
			}
		}
		if v, _ := props.Get("pvp"); v != "" {
			t.Logf("server.properties has pvp=%s again (26.3 had no such key)", v)
		}

		// RCON opens just after the "Done" line (FACTS.md), so the first
		// try can come a moment too early, as the manager's can.
		var res game.ProbeResult
		for range 20 {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			res, err = srv.(game.Prober).Probe(ctx, s.Record())
			cancel()
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil || !res.Up || res.Count != 0 || res.Names == nil {
			t.Errorf("RCON list = %+v, %v; want up with nobody on", res, err)
		}

		st := s.State()
		j := srv.Join(game.JoinInput{Rec: ptr(s.Record()), State: &st, Saved: s.Env().Store.Get(), PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}})
		if !strings.HasPrefix(j.Address, "203.0.113.7") || j.How != "address" || !strings.Contains(strings.Join(j.Steps, " "), st.Version) {
			t.Errorf("Join = %+v (version %q)", j, st.Version)
		}

		level := filepath.Join(s.Env().Dirs.Saves, "Real", "level.dat")
		before, err := os.Stat(level)
		if err != nil {
			t.Fatalf("the new world has no level.dat: %v", err)
		}
		time.Sleep(1100 * time.Millisecond) // so a save shows in the file time on every file system
		s.Stop()
		after, err := os.Stat(level)
		if err != nil || !after.ModTime().After(before.ModTime()) {
			t.Errorf("level.dat wasn't saved by the stop (%v → %v, %v)", before.ModTime(), after.ModTime(), err)
		}

		s.StartAgain()
		s.WaitEvent("online", "", 8*time.Minute)
		if s.Record().World != "Real" {
			t.Errorf("the second start ran %q", s.Record().World)
		}
		s.Stop()
	})

	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, Game{}, gametest.Options{Real: true, Change: agreed})
	})
}

func ptr[T any](v T) *T { return &v }
