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
	"maps"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
	"unicode/utf16"

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
		own := ownFavorites()
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Takeover", NewWorld: small})
		ready := s.Plan().ReadyWait + 5*time.Minute
		s.WaitEvent("online", "", ready)
		port := s.Record().Port
		t.Logf("installed: %+v", must(s.Server().Build()))

		// Making the world wrote favorites.json in the server folder
		// (-savedirectory), and left the owner's own Terraria folder alone.
		ours := filepath.Join(s.Env().Dirs.Server, "favorites.json")
		if _, err := os.Stat(ours); err != nil {
			t.Errorf("the new world's favorites.json isn't in the server folder: %v", err)
		}
		for path, was := range own {
			if now := stamp(path); !now.Equal(was) {
				t.Errorf("the server changed the owner's own %s (%v, before %v): -savedirectory didn't keep it out", path, now, was)
			}
		}
		t.Logf("FACTS: the new world's favorites.json is %s; the owner's own %q untouched", ours, slices.Sorted(maps.Keys(own)))

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

	// What the Windows server does with its console and with Ctrl+C, for
	// FACTS.md (3, 6 and 11). Its program sets Console.InputEncoding to
	// UTF-16 on Windows (Program.InitializeConsoleOutput; UTF-8 on the Mac
	// and Linux), so a line typed as UTF-8 bytes on its piped stdin never
	// ends: "save" and "exit" do nothing. And its console handler
	// (WindowsLaunch.ConsoleCtrlCheck) answers every Ctrl+C itself and
	// carries on. This checks both on the real server, so the facts the
	// adapter's Windows stop text and the lead's console change rest on
	// are seen, not only read from the program.
	t.Run("Windows console", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("only the Windows server reads its console as UTF-16 and ignores Ctrl+C")
		}
		s := gametest.Start(t, Game{}, gametest.Options{Real: true, World: "Console", NewWorld: small})
		s.WaitEvent("online", "", s.Plan().ReadyWait+5*time.Minute)
		wld := filepath.Join(s.Env().Dirs.Saves, "Console.wld")
		p, err := platform.OpenProc(s.Record().PID)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()

		// 1. The save line as gametest (and so far the manager) types it:
		// its UTF-8 bytes and a line feed.
		time.Sleep(1100 * time.Millisecond) // a newer time stamp than the new world's
		first := modTime(t, wld)
		encoded, took := savedAfter(t, s, "save", 15*time.Second)
		if encoded {
			// gametest already types lines in the encoding the server
			// reads (the lead's change): nothing to work around.
			t.Logf("FACTS: \"save\" typed through gametest saved after %v: console lines reach the Windows server in its own encoding", took)
		} else {
			t.Logf("FACTS: \"save\" typed as UTF-8 bytes: no save within 15 s, world file still %v", modTime(t, wld))
			// 2. The same line as UTF-16 code units.
			ok, took := savedAfter(t, s, utf16Line("save"), time.Minute)
			t.Logf("FACTS: \"save\" typed as UTF-16LE: saved %v after %v; world file %v, before %v", ok, took, modTime(t, wld), first)
			if !ok {
				t.Errorf("\"save\" typed as UTF-16LE didn't save either: the Windows server's console isn't what FACTS.md says")
			}
		}

		// 3. Ctrl+C, while the app still has the console: the server
		// should go on running. As gametest does, this test first makes
		// sure the Ctrl+C sent through the server's console can't also
		// stop the test itself.
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
		err = p.Interrupt()
		if gone(p, 30*time.Second) {
			code, _ := p.Wait()
			t.Errorf("FACTS: Ctrl+C (%v) ended the Windows server with code %d, world file %v: FACTS.md and the Windows stop text say it doesn't, so fix them", err, code, modTime(t, wld))
			return
		}
		t.Logf("FACTS: Ctrl+C (%v): the Windows server still runs 30 s later, as its console handler says", err)

		// 4. "exit" in the encoding that works: it saves and ends it.
		before := modTime(t, wld)
		time.Sleep(1100 * time.Millisecond)
		exit, how := "exit", "as gametest types it"
		if !encoded {
			exit, how = utf16Line("exit"), "as UTF-16LE"
		}
		if err := s.Console(exit); err != nil {
			t.Fatal(err)
		}
		if !gone(p, time.Minute) {
			t.Fatalf("\"exit\" typed %s didn't end the Windows server within a minute", how)
		}
		code, _ := p.Wait()
		after := modTime(t, wld)
		t.Logf("FACTS: \"exit\" typed %s ended the Windows server with code %d; world file %v, before %v", how, code, after, before)
		if !after.After(before) {
			t.Errorf("\"exit\" didn't save the world (%v, before %v)", after, before)
		}
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

// savedAfter types line on the server's console and says whether the
// Monitor saw the world saved within the wait, and how long that took.
func savedAfter(t *testing.T, s *gametest.Session, line string, within time.Duration) (bool, time.Duration) {
	t.Helper()
	before := s.State().LastSave
	start := time.Now()
	if err := s.Console(line); err != nil {
		t.Fatal(err)
	}
	for time.Since(start) < within {
		if s.State().LastSave.After(before) {
			return true, time.Since(start).Round(100 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false, within
}

// ownFavorites are the favorites.json files of the owner's own Terraria on
// this computer, beside its Worlds folders (not Steam's cloud ones), with
// their time stamps now (zero when there is none).
func ownFavorites() map[string]time.Time {
	home, _ := os.UserHomeDir()
	out := map[string]time.Time{}
	for _, w := range localWorlds(home) {
		if filepath.Base(w) == "Worlds" {
			f := filepath.Join(filepath.Dir(w), "favorites.json")
			out[f] = stamp(f)
		}
	}
	return out
}

// stamp is when a file last changed, or zero when it isn't there.
func stamp(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// gone says whether the process ends within the wait.
func gone(p *platform.Proc, within time.Duration) bool {
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if p.Exited() {
			return true
		}
	}
	return p.Exited()
}

// utf16Line is a console line as Terraria's Windows server reads it:
// UTF-16 little-endian code units, ending with a line feed.
//
// gametest's Console writes the line and then one byte more, a line feed
// (0x0A). In UTF-16 that byte is half a code unit, which the server's
// reader keeps until the next byte comes. So this line starts with the
// other half (0x00): with the byte left over from the line before, it
// makes a line feed, which ends whatever came before: the UTF-8 "save",
// read as two meaningless characters, which the server answers with
// "Invalid command.", or an empty line, which it ignores (both seen with
// the Linux server, whose command code is the same). So each line leaves
// the same half unit behind for the next one; the first is left by the
// UTF-8 "save" and its line feed, five bytes. Only this test types lines
// that way: the app needs the console's encoding in the game contract
// (FACTS.md, 3 and 11).
func utf16Line(text string) string {
	b := []byte{0x00}
	for _, u := range utf16.Encode([]rune(text + "\n")) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return string(b)
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
