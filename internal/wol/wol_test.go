package wol

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestPacket(t *testing.T) {
	mac, err := ParseMAC(" 3c:07:54:5a:21:9f\n")
	if err != nil {
		t.Fatal(err)
	}
	p := Packet(mac, nil)
	if len(p) != 102 || !bytes.Equal(p[:6], []byte{255, 255, 255, 255, 255, 255}) || !bytes.Equal(p[96:], mac) {
		t.Fatalf("packet = % x", p)
	}
	if got, tag, ok := Target(p); !ok || !bytes.Equal(got, mac) || len(tag) != 0 {
		t.Fatalf("target = %v %x %v", got, tag, ok)
	}
	tagged := Packet(mac, []byte{1, 2, 3, 4, 5, 6, 7})
	if got, tag, ok := Target(tagged); len(tagged) != 108 || !ok || !bytes.Equal(got, mac) || !bytes.Equal(tag, []byte{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("tagged target = %v %x %v", got, tag, ok)
	}
	broken := bytes.Clone(p)
	broken[50] ^= 1
	for _, b := range [][]byte{p[:101], broken, append([]byte{0}, p[1:]...)} {
		if _, _, ok := Target(b); ok {
			t.Errorf("%x read as a magic packet", b)
		}
	}
	for _, bad := range []string{"", "3C:07:54", "nope", "3C:07:54:5A:21:9F:00:11"} {
		if _, err := ParseMAC(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSend(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mac, _ := ParseMAC("3C:07:54:5A:21:9F")
	if err := Send(mac, []byte{9}, []string{"203.0.113.1:not-a-port", conn.LocalAddr().String()}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 200)
	n, _, err := conn.ReadFrom(buf)
	if got, tag, ok := Target(buf[:n]); err != nil || !ok || !bytes.Equal(got, mac) || !bytes.Equal(tag, []byte{9}) {
		t.Fatalf("received %x (%v)", buf[:n], err)
	}
	if err := Send(mac, nil, []string{"203.0.113.1:not-a-port"}); err == nil {
		t.Fatal("no error when nothing was sent")
	}
	if b := Broadcasts(); len(b) == 0 || b[0] != "255.255.255.255:9" {
		t.Fatalf("broadcasts = %v", b)
	}
}
