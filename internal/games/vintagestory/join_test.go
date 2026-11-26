package vintagestory

import (
	"slices"
	"strings"
	"testing"

	"valheim-easy-server/internal/game"
)

func TestJoin(t *testing.T) {
	srv := Game{}.New(testEnv(t)).(*server)
	sv := settings("W")
	sv.Ports["game"] = defaultPort

	// Stopped, nothing installed yet: still steps, no version.
	j := srv.Join(game.JoinInput{Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20"}, Router: map[string]string{"game": "open"}})
	if j.How != "address" || j.Address != "203.0.113.7" || j.Port != defaultPort || !j.Router || j.Public || j.Problem != "" {
		t.Errorf("stopped: %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:42420"}) {
		t.Errorf("LAN = %q", j.LAN)
	}
	if j.Password != "s3cret&<word>" || len(j.Steps) != 3 || !strings.Contains(j.Steps[1], "203.0.113.7") || !strings.Contains(j.Hint, "Vintage Story") {
		t.Errorf("stopped: %+v", j)
	}

	// Running: the port and password the server started with, its
	// version, and the port in the address when it isn't the game's own.
	rec := &game.Record{Port: 42433, Password: "started-with"}
	st := &game.RunState{Version: "1.22.7", Ready: true}
	j = srv.Join(game.JoinInput{Rec: rec, State: st, Saved: sv, PublicIP: "203.0.113.7", LAN: []string{"192.168.1.20", "10.0.0.5"},
		Router: map[string]string{"game": "failed"}})
	if j.Address != "203.0.113.7:42433" || j.Port != 42433 || j.Password != "started-with" {
		t.Errorf("running: %+v", j)
	}
	if !slices.Equal(j.LAN, []string{"192.168.1.20:42433", "10.0.0.5:42433"}) {
		t.Errorf("LAN = %q", j.LAN)
	}
	if !strings.Contains(j.Steps[2], "1.22.7") || !strings.Contains(j.Problem, "42433") {
		t.Errorf("running: %+v", j)
	}

	// No public address known.
	j = srv.Join(game.JoinInput{Saved: sv})
	if j.Address != "" || len(j.Steps) == 0 {
		t.Errorf("no address: %+v", j)
	}
}

func TestInfo(t *testing.T) {
	in := Game{}.Info()
	if in.ID != "vintagestory" || in.Password.Use != "required" || in.OS["windows"] != game.Runs || in.OS["darwin"] != "" {
		t.Errorf("Info = %+v", in)
	}
	if in.NeedsText["darwin"] != "Needs Windows" {
		t.Errorf("NeedsText = %v", in.NeedsText)
	}
	if len(in.Ports) != 1 || in.Ports[0].Default != 42420 || !slices.Equal(in.Ports[0].Protos, []string{"tcp", "udp"}) || !in.Ports[0].Public {
		t.Errorf("Ports = %+v", in.Ports)
	}
	if g, ok := game.Get(ID); !ok || g.Info().Name != "Vintage Story" {
		t.Error("the game isn't registered")
	}
}
