//go:build realserver

// The real-server check (spec 5.3 and 7.4). The CI matrix runs it on
// Windows and an Intel Mac from realserver.json:
//
//	go test -tags realserver -run TestRealServer -v -count=1 -timeout 60m ./internal/games/terraria/
//
// It downloads the real server from terraria.org (57 MB; on Windows also
// Microsoft's XNA Framework, which installs without a prompt because the
// runner is an administrator) and plays it through gametest, without the
// manager. It also runs on Linux, where the zip's Linux server is used,
// though Linux isn't a system the app is offered on.

package terraria

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
	"valheim-easy-server/internal/platform"
)

func TestRealServer(t *testing.T) {
	small := game.Values{"size": "small", "difficulty": "classic"}

	// Everything every adapter must do, against the real server: install,
	// a new world, online, the save line, Join, the stop by "exit", a
	// second start on the same world, a takeover whose stop has no
	// console and goes to Ctrl+C, and a backup that restores.
	t.Run("conformance", func(t *testing.T) {
		gametest.Conformance(t, Game{}, gametest.Options{Real: true, World: "RealCheck", NewWorld: small})
	})

	// The parts particular to Terraria (spec 5.3).
	t.Run("console, takeover and Ctrl+C", func(t *testing.T) {
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Takeover", NewWorld: small})
		ready := s.Plan().ReadyWait + 5*time.Minute
		s.WaitEvent("online", "", ready)
		port := s.Record().Port
		t.Logf("installed: %+v", must(s.Server().Build()))

		// The server answers on its port: a missing native library would
		// show here, not on the owner's computer.
		if err := handshake(port); err != nil {
			t.Fatalf("the server doesn't answer on TCP port %d: %v", port, err)
		}
		// For FACTS.md: what the system's tables say about the port.
		for _, proto := range []string{"tcp", "udp"} {
			inUse, pid := platform.PortInUse(proto, port)
			t.Logf("FACTS: PortInUse(%q, %d) = %v, pid %d (the server is %d)", proto, port, inUse, pid, s.Record().PID)
		}

		world := s.Server().Worlds()
		wld := s.Env().Dirs.Saves + string(os.PathSeparator) + "Takeover.wld"
		first := modTime(t, wld)

		// The app's save line saves without stopping.
		time.Sleep(1100 * time.Millisecond) // a newer time stamp than the first save's
		if err := s.Console("save"); err != nil {
			t.Fatal(err)
		}
		s.WaitEvent("saved", "", time.Minute)
		saved := modTime(t, wld)
		if !saved.After(first) {
			t.Errorf("\"save\" didn't write the world (%v, before %v)", saved, first)
		}

		// "exit" saves and ends it.
		time.Sleep(1100 * time.Millisecond)
		s.Stop()
		if exited := modTime(t, wld); !exited.After(saved) {
			t.Errorf("\"exit\" didn't save the world (%v, before %v)", exited, saved)
		}

		// A takeover: the app that started it is gone with its console,
		// so the stop goes straight to Ctrl+C, which saves nothing.
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		before, _ := world.Get("Takeover")
		s.Takeover()
		start := time.Now()
		s.Stop()
		t.Logf("the stop after the takeover took %v", time.Since(start).Round(time.Second))
		if after, err := world.Get("Takeover"); err != nil || after.Size != before.Size {
			t.Logf("after Ctrl+C the world is %+v (%v); before %+v", after, err, before)
		}

		// The world from the last save is intact: it loads again.
		s.StartAgain()
		s.WaitEvent("online", "", ready)
		if st := s.State(); !st.Ready || st.Version == "" {
			t.Errorf("after loading the world again: %+v", st)
		}
		s.Stop()
	})

	// Repair over an XNA that Windows lists but that is damaged: the one
	// state in which ExitHint sends the owner to Repair over an XNA error.
	// Runs last, as it takes one of XNA's files away for a moment.
	t.Run("XNA repair", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("only the Windows server uses XNA")
		}
		if !haveXNA() {
			t.Fatal("Windows doesn't list XNA after the install")
		}
		// Damage it: take its main file out of the global assembly cache.
		// If Windows won't let the runner, the repair still runs.
		root := os.Getenv("SystemRoot")
		found, _ := filepath.Glob(filepath.Join(root, "Microsoft.NET", "assembly", "GAC_*", "Microsoft.Xna.Framework", "*", "Microsoft.Xna.Framework.dll"))
		removed := ""
		for _, f := range found {
			if err := os.Remove(f); err != nil {
				t.Logf("FACTS: couldn't remove %s for the repair check: %v", f, err)
				continue
			}
			removed = f
			break
		}
		t.Logf("FACTS: XNA's main file: %q, removed %q", found, removed)
		var steps []string
		err := installXNA(context.Background(), true, func(p game.Progress) { steps = append(steps, p.Step) })
		t.Logf("FACTS: XNA repair: %v; listed after: %v; steps %q", err, haveXNA(), steps)
		if err != nil {
			t.Fatal(err)
		}
		if removed != "" {
			if _, err := os.Stat(removed); err != nil {
				t.Errorf("the repair didn't put back %s: %v", removed, err)
			}
		}
	})
}

func must[T any](v T, ok bool) T {
	return v
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the world file: %v", err)
	}
	return st.ModTime()
}

// handshake connects as a Terraria client of release 0 and waits for the
// server's answer, which for a client of another version is a message
// turning it away. Any answer proves the server's network side works; the
// release number isn't needed, so a new Terraria version can't break it.
func handshake(port int) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	hello := "Terraria0"
	var msg bytes.Buffer
	binary.Write(&msg, binary.LittleEndian, uint16(3+1+len(hello)))
	msg.WriteByte(1) // "connect request"
	msg.WriteByte(byte(len(hello)))
	msg.WriteString(hello)
	if _, err := c.Write(msg.Bytes()); err != nil {
		return err
	}
	var head [3]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return fmt.Errorf("no answer: %v", err)
	}
	return nil
}
