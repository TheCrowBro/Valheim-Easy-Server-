//go:build windows

package vintagestory

import (
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

// TestFromCodePage: Windows reads a console line in the code pages a
// hidden console can start with ("ü" is 0x81 in 437 and 850, 0xFC in 1252).
func TestFromCodePage(t *testing.T) {
	for _, c := range []struct {
		cp  uint32
		raw string
	}{{437, "J\x81rgen"}, {850, "J\x81rgen"}, {1252, "J\xfcrgen"}} {
		if got, ok := fromCodePage(c.cp, c.raw); !ok || got != "Jürgen" {
			t.Errorf("fromCodePage(%d, %q) = %q, %v; want \"Jürgen\"", c.cp, c.raw, got, ok)
		}
	}
	if _, ok := fromCodePage(0, "J\x81rgen"); ok {
		t.Error("fromCodePage read a line with no code page")
	}
	if got := consoleText("Jürgen joined"); got != "Jürgen joined" {
		t.Errorf("consoleText changed a UTF-8 line: %q", got)
	}
}

// TestMonitorOEMName: on a computer whose consoles start in code page 437
// or 850 (most English and western European Windows), a join line with
// "Jürgen" as the server writes it there gives Jürgen, and his leave line
// takes him off the list.
func TestMonitorOEMName(t *testing.T) {
	cp := oemCodePage()
	if cp != 437 && cp != 850 {
		t.Skipf("this computer's consoles start in code page %d", cp)
	}
	m := (&server{}).Watch(game.Record{}, nil)
	now := time.Now()
	l := m.Line("1.12.2026 11:00:00 [Server Event] J\x81rgen [::ffff:192.0.2.7]:50000 joins.\r\n", now)
	if len(l.Events) != 1 || l.Events[0].Kind != "join" || l.Events[0].Name != "Jürgen" {
		t.Fatalf("join line gave %+v", l.Events)
	}
	l = m.Line("1.12.2026 11:01:00 [Server Event] Player J\x81rgen left.\r\n", now)
	if len(l.Events) != 1 || l.Events[0].Kind != "leave" || l.Events[0].Name != "Jürgen" {
		t.Fatalf("leave line gave %+v", l.Events)
	}
}
