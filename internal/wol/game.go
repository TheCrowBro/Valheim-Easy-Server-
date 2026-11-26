package wol

import (
	"bytes"
	"net"
)

// A wake packet can say which game someone asked for (spec 6.4): today's
// packet (six 0xFF bytes, the address 16 times, then the 6-byte tag),
// followed by the marker "EGS1", one length byte and the instance id:
//
//	FF×6 | MAC×16 | tag (6) | "EGS1" | len (1) | id (len bytes)
//
// The id goes after the tag, never in it, so an older copy of the app,
// which reads only the tag (Target), still sees the same tag and starts
// Valheim once per request, as it always did. Network adapters ignore
// anything after the address (or after a SecureOn password), so the extra
// bytes don't change whether the packet wakes the computer.

// gameMarker starts the game part. The digit is its version: a later
// layout would use another one, which this version reads as unreadable
// (UnreadableGame) and so ignores, rather than taking it for an old packet
// and starting the legacy game.
const gameMarker = "EGS1"

// gameFamily is what every version of the marker starts with.
const gameFamily = "EGS"

// tagEnd is where the tag ends and the game part starts.
const tagEnd = 102 + 6

// maxIDLen is the longest instance id a packet carries; it matches the
// servers.json rule (a letter, then up to 63 letters, digits or hyphens).
const maxIDLen = 64

// UnreadableGame is what GameOf returns for a packet that says it is for a
// game but whose id can't be read (cut short, not a possible id, or a
// later layout). It is never an instance id, so nothing is started for it.
const UnreadableGame = "?"

// PacketFor is the wake packet for the instance id. With no id it is
// exactly today's packet (Packet), which every copy of the app reads as a
// request for the legacy game. With an id, the tag is made exactly 6 bytes
// long (cut, or padded with zero bytes) so the id always starts at the same
// place; the waker's tags are 6 random bytes, which stay as they are. An
// id that can't be an instance id is sent as unreadable, so the app starts
// nothing rather than a wrong game.
func PacketFor(mac net.HardwareAddr, tag []byte, id string) []byte {
	if id == "" {
		return Packet(mac, tag)
	}
	t := make([]byte, 6)
	copy(t, tag)
	p := Packet(mac, t)
	p = append(p, gameMarker...)
	if !validID(id) {
		return append(p, 0)
	}
	p = append(p, byte(len(id)))
	return append(p, id...)
}

// GameOf reads which instance a wake packet asks for:
//
//   - "" when it names none: today's packets, tagged (from an older waker:
//     the legacy game, as today) or untagged (the KVM's own Wake-on-LAN
//     button), and anything that isn't a magic packet at all;
//   - the instance id, when it carries a readable one;
//   - UnreadableGame when it says it carries one that can't be read.
//
// Bytes after the id are ignored, so a later version may add more there.
func GameOf(b []byte) string {
	if _, _, ok := Target(b); !ok || len(b) <= tagEnd {
		return ""
	}
	rest := b[tagEnd:]
	if !bytes.HasPrefix(rest, []byte(gameMarker)) {
		if bytes.HasPrefix(rest, []byte(gameFamily)) {
			return UnreadableGame // a layout from a later version
		}
		return "" // not ours: a sender that adds its own bytes
	}
	rest = rest[len(gameMarker):]
	if len(rest) < 1 {
		return UnreadableGame
	}
	n := int(rest[0])
	rest = rest[1:]
	if n == 0 || len(rest) < n {
		return UnreadableGame
	}
	id := string(rest[:n])
	if !validID(id) {
		return UnreadableGame
	}
	return id
}

// validID is servers.ValidID's rule (this package can't import it: the
// waker on the JetKVM is built from it too, and stays small): a lower-case
// letter, then up to 63 lower-case letters, digits and hyphens.
func validID(id string) bool {
	if len(id) == 0 || len(id) > maxIDLen || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
