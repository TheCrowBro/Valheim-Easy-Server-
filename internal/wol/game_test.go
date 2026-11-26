package wol

import (
	"bytes"
	"strings"
	"testing"
)

// TestPacketForRoundTrip: a packet for a game carries its id, and an older
// app, which reads only the tag, still sees today's packet with the same
// tag (spec 6.4).
func TestPacketForRoundTrip(t *testing.T) {
	mac, _ := ParseMAC("3C:07:54:5A:21:9F")
	tag := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02}
	for _, id := range []string{"valheim", "minecraft", "minecraft-2", "a", "x" + strings.Repeat("y", 63)} {
		p := PacketFor(mac, tag, id)
		if got := GameOf(p); got != id {
			t.Errorf("GameOf(PacketFor(%q)) = %q", id, got)
		}
		// Old tag unchanged: the packet starts with exactly today's one.
		if !bytes.HasPrefix(p, Packet(mac, tag)) {
			t.Errorf("%s: doesn't start with today's packet: % x", id, p)
		}
		if got, gotTag, ok := Target(p); !ok || !bytes.Equal(got, mac) || !bytes.Equal(gotTag, tag) {
			t.Errorf("%s: an older app reads %v %x %v", id, got, gotTag, ok)
		}
		if want := 108 + 4 + 1 + len(id); len(p) != want {
			t.Errorf("%s: %d bytes, want %d", id, len(p), want)
		}
	}

	// No id: exactly today's packets, tagged and untagged, which name no game.
	for _, tg := range [][]byte{nil, tag} {
		p := PacketFor(mac, tg, "")
		if !bytes.Equal(p, Packet(mac, tg)) {
			t.Errorf("PacketFor with no id = % x", p)
		}
		if got := GameOf(p); got != "" {
			t.Errorf("GameOf(today's packet, tag %x) = %q", tg, got)
		}
	}

	// A short tag is padded and a long one cut, so the id is always found.
	p := PacketFor(mac, []byte{7, 8}, "terraria")
	if _, gotTag, _ := Target(p); !bytes.Equal(gotTag, []byte{7, 8, 0, 0, 0, 0}) || GameOf(p) != "terraria" {
		t.Errorf("short tag: %x, %q", gotTag, GameOf(p))
	}
	p = PacketFor(mac, []byte{1, 2, 3, 4, 5, 6, 7, 8}, "terraria")
	if _, gotTag, _ := Target(p); !bytes.Equal(gotTag, []byte{1, 2, 3, 4, 5, 6}) || GameOf(p) != "terraria" {
		t.Errorf("long tag: %x, %q", gotTag, GameOf(p))
	}

	// An id that can't be an instance id goes out as unreadable, so the app
	// starts nothing.
	for _, id := range []string{"Minecraft", "../x", "a b", "2fast", "x" + strings.Repeat("y", 64)} {
		if got := GameOf(PacketFor(mac, tag, id)); got != UnreadableGame {
			t.Errorf("GameOf(PacketFor(%q)) = %q", id, got)
		}
	}
}

// TestGameOfOddPackets: packets cut short or from a later layout name an
// unreadable game (nothing starts), while bytes that aren't ours, and
// things that aren't wake packets at all, name none.
func TestGameOfOddPackets(t *testing.T) {
	mac, _ := ParseMAC("3C:07:54:5A:21:9F")
	tag := []byte("abcdef")
	full := PacketFor(mac, tag, "minecraft")
	for cut := 109; cut < len(full); cut++ { // anything after the tag, short of the whole id
		want := UnreadableGame
		if cut < 108+len(gameFamily) {
			want = "" // "E", "EG": not even the marker's family
		}
		if got := GameOf(full[:cut]); got != want {
			t.Errorf("cut at %d: %q, want %q", cut, got, want)
		}
	}
	today := Packet(mac, tag)
	with := func(s string) []byte { return append(bytes.Clone(today), s...) }
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"zero length":    {with("EGS1\x00"), UnreadableGame},
		"later layout":   {with("EGS2\x09minecraft"), UnreadableGame},
		"bad id bytes":   {with("EGS1\x03a\x00b"), UnreadableGame},
		"extra after id": {with("EGS1\x09minecraft\x01\x02"), "minecraft"},
		"someone else's": {with("\x00\x00\x00\x00"), ""},
		"short tag":      {append(Packet(mac, []byte{1}), "EGS1\x01a"...), ""}, // never made by PacketFor
		"not a packet":   {append([]byte("hello"), with("EGS1\x01a")[5:]...), ""},
		"empty":          {nil, ""},
	} {
		if got := GameOf(c.b); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	// A wrong address repeat isn't a wake packet, whatever follows.
	broken := bytes.Clone(full)
	broken[40] ^= 1
	if got := GameOf(broken); got != "" {
		t.Errorf("broken packet: %q", got)
	}
}
