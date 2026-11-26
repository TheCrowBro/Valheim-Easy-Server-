package a2s

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slack is how much longer than its limit a call may take before a test
// calls it hung: CI machines can be slow, but not this slow.
const slack = 5 * time.Second

// enshrouded is an answer like a real server's, with an app id too big
// for the answer's two-byte field.
var enshrouded = Info{
	Name:       "Our server",
	Map:        "Embervale",
	Folder:     "enshrouded",
	Game:       "Enshrouded",
	AppID:      1203620,
	Players:    3,
	MaxPlayers: 16,
	Bots:       0,
	Version:    "0.8.0.1",
}

// startResponder starts a TestResponder that closes when the test ends.
func startResponder(t *testing.T, challenge bool, info func() Info) *TestResponder {
	t.Helper()
	r, err := NewTestResponder("127.0.0.1:0", challenge, info)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// query asks addr with a generous limit.
func query(t *testing.T, addr string) (Info, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), slack)
	defer cancel()
	return Query(ctx, addr)
}

// udpServer answers each packet it gets with what answer returns (nothing
// when it returns nil), until the test ends.
func udpServer(t *testing.T, answer func(req []byte) []byte) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			if out := answer(bytes.Clone(buf[:n])); out != nil {
				conn.WriteTo(out, from)
			}
		}
	})
	t.Cleanup(func() {
		conn.Close()
		wg.Wait()
	})
	return conn.LocalAddr().String()
}

func TestQuery(t *testing.T) {
	for _, challenge := range []bool{false, true} {
		r := startResponder(t, challenge, func() Info { return enshrouded })
		got, err := query(t, r.Addr)
		if err != nil {
			t.Fatalf("challenge %v: %v", challenge, err)
		}
		if got != enshrouded {
			t.Errorf("challenge %v: got %+v, want %+v", challenge, got, enshrouded)
		}
	}
}

// TestQuerySeesCurrentInfo: each query gets info's answer at that moment,
// as players come and go.
func TestQuerySeesCurrentInfo(t *testing.T) {
	var players atomic.Int32
	r := startResponder(t, true, func() Info {
		return Info{Name: "Valheim", AppID: 892970, Players: int(players.Load()), MaxPlayers: 10}
	})
	for _, n := range []int32{0, 2, 1, 0} {
		players.Store(n)
		got, err := query(t, r.Addr)
		if err != nil {
			t.Fatal(err)
		}
		if got.Players != int(n) || got.AppID != 892970 || got.MaxPlayers != 10 {
			t.Errorf("with %d players: %+v", n, got)
		}
	}
}

// TestResponderChallenge talks to the responder packet by packet: with
// challenge set, only a query carrying the challenge it gave gets the
// answer; without, every query is answered at once.
func TestResponderChallenge(t *testing.T) {
	r := startResponder(t, true, func() Info { return enshrouded })
	conn, err := net.Dial("udp", r.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(slack))
	ask := func(challenge []byte) []byte {
		t.Helper()
		if _, err := conn.Write(request(challenge)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return buf[:n]
	}

	first := ask(nil)
	if len(first) != 9 || first[4] != kindChallenge || binary.LittleEndian.Uint32(first) != headerWhole {
		t.Fatalf("a query without a challenge got %x; want a challenge", first)
	}
	challenge := first[5:9]
	wrong := []byte{challenge[0] ^ 1, challenge[1], challenge[2], challenge[3]}
	if again := ask(wrong); again[4] != kindChallenge || !bytes.Equal(again[5:9], challenge) {
		t.Fatalf("a query with a wrong challenge got %x; want the same challenge again", again)
	}
	if again := ask([]byte{0xFF, 0xFF, 0xFF, 0xFF}); again[4] != kindChallenge {
		t.Fatalf("a query asking for a challenge got %x", again)
	}
	answer := ask(challenge)
	if answer[4] != kindInfo {
		t.Fatalf("a query with the challenge got %x; want the answer", answer)
	}
	if got, err := parseInfo(answer[5:]); err != nil || got != enshrouded {
		t.Fatalf("the answer reads as %+v, %v", got, err)
	}

	open := startResponder(t, false, func() Info { return enshrouded })
	conn2, err := net.Dial("udp", open.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	conn2.SetDeadline(time.Now().Add(slack))
	conn2.Write(request(nil))
	buf := make([]byte, 2048)
	if n, err := conn2.Read(buf); err != nil || buf[4] != kindInfo {
		t.Fatalf("without a challenge, the first answer was %x, %v", buf[:n], err)
	}
}

// TestQueryAnswersChallenge: the challenge round trip against a server
// that checks it, as Steam servers do.
func TestQueryAnswersChallenge(t *testing.T) {
	challenge := []byte{0x12, 0x34, 0x56, 0x78}
	var queries, withChallenge atomic.Int32
	addr := udpServer(t, func(req []byte) []byte {
		got, ok := parseRequest(req)
		if !ok {
			return nil
		}
		queries.Add(1)
		if !bytes.Equal(got, challenge) {
			return append([]byte{0xFF, 0xFF, 0xFF, 0xFF, kindChallenge}, challenge...)
		}
		withChallenge.Add(1)
		return encodeInfo(enshrouded)
	})
	got, err := query(t, addr)
	if err != nil || got != enshrouded {
		t.Fatalf("Query = %+v, %v", got, err)
	}
	// Normally exactly 2 and 1; more only if a slow machine made Query
	// send one again.
	if q, w := queries.Load(), withChallenge.Load(); q < 2 || w < 1 {
		t.Errorf("the server got %d queries, %d with the challenge; want 2 and 1", q, w)
	}
}

// TestQueryAsksAgain: a lost query (or answer) is sent again.
func TestQueryAsksAgain(t *testing.T) {
	var seen atomic.Int32
	addr := udpServer(t, func(req []byte) []byte {
		if seen.Add(1) == 1 {
			return nil // lost on the way
		}
		return encodeInfo(enshrouded)
	})
	start := time.Now()
	got, err := query(t, addr)
	if err != nil || got != enshrouded {
		t.Fatalf("Query = %+v, %v", got, err)
	}
	// A little under resendEvery, so a coarse system clock can't fail it.
	if d := time.Since(start); d < resendEvery*4/5 {
		t.Errorf("answered after %v, before the query could have been sent again", d)
	}
}

func TestQueryTimeouts(t *testing.T) {
	addr := udpServer(t, func([]byte) []byte { return nil }) // never answers

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Query(ctx, addr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Query past its deadline = %v; want DeadlineExceeded", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > slack {
		t.Errorf("Query gave up after %v; want about 300ms", d)
	}

	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := Query(ctx, addr); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query cancelled = %v; want Canceled", err)
	}
	if d := time.Since(start); d > slack {
		t.Errorf("Query took %v to notice it was cancelled", d)
	}
	if _, err := Query(ctx, addr); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query with an ended ctx = %v; want Canceled", err)
	}
}

// TestDefaultTimeout: with no deadline on ctx, Query gives up after 2
// seconds (shortened here).
func TestDefaultTimeout(t *testing.T) {
	if defaultTimeout != 2*time.Second {
		t.Errorf("defaultTimeout = %v; Query's comment promises 2 seconds", defaultTimeout)
	}
	old := defaultTimeout
	defaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { defaultTimeout = old })

	addr := udpServer(t, func([]byte) []byte { return nil })
	start := time.Now()
	if _, err := Query(context.Background(), addr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Query with no deadline = %v; want DeadlineExceeded", err)
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > slack {
		t.Errorf("Query gave up after %v; want about 300ms", d)
	}
}

func TestNothingListening(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// Linux and macOS report the closed port at once; Windows may only
	// time out. Either way it's an error, within the limit.
	if _, err := Query(ctx, addr); err == nil {
		t.Fatal("Query with nothing listening worked")
	}
}

func TestBadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer []byte
		want   error
	}{
		{"not A2S", []byte("hello there"), errNotSteam},
		{"too short", []byte{0xFF, 0xFF, 0xFF, 0xFF}, errNotSteam},
		{"unknown kind", []byte{0xFF, 0xFF, 0xFF, 0xFF, 'Z', 0}, errNotSteam},
		{"short challenge", []byte{0xFF, 0xFF, 0xFF, 0xFF, kindChallenge, 1, 2}, errNotSteam},
		{"cut-off answer", encodeInfo(enshrouded)[:12], errNotSteam},
		{"split answer", []byte{0xFE, 0xFF, 0xFF, 0xFF, 1, 0, 0, 0, 2, 0, 0xE0, 4}, errSplit},
	} {
		addr := udpServer(t, func([]byte) []byte { return tc.answer })
		start := time.Now()
		_, err := query(t, addr)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v; want %v", tc.name, err, tc.want)
		}
		if d := time.Since(start); d > slack/2 {
			t.Errorf("%s: took %v to notice", tc.name, d)
		}
	}
}

// TestEndlessChallenges: a server that asks for a new challenge every time
// is given up on, rather than chased until the deadline.
func TestEndlessChallenges(t *testing.T) {
	var n atomic.Uint32
	addr := udpServer(t, func([]byte) []byte {
		return binary.LittleEndian.AppendUint32([]byte{0xFF, 0xFF, 0xFF, 0xFF, kindChallenge}, n.Add(1))
	})
	if _, err := query(t, addr); !errors.Is(err, errChasing) {
		t.Fatalf("Query = %v; want errChasing", err)
	}
	if got := n.Load(); got < maxChallenges+1 {
		t.Errorf("the server was asked %d times; want %d", got, maxChallenges+1)
	}
}

func TestResponderClose(t *testing.T) {
	r := startResponder(t, false, func() Info { return enshrouded })
	if _, err := query(t, r.Addr); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := Query(ctx, r.Addr); err == nil {
		t.Fatal("a closed responder still answered")
	}
}

// TestResponderLimits: counts beyond a byte are capped, app ids that fit
// two bytes still come through the extra data, and a nil info answers.
func TestResponderLimits(t *testing.T) {
	r := startResponder(t, false, func() Info {
		return Info{Name: "big\x00name", AppID: 440, Players: 300, MaxPlayers: -5, Bots: 256}
	})
	got, err := query(t, r.Addr)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Info{Name: "bigname", AppID: 440, Players: 255, MaxPlayers: 0, Bots: 255}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	empty := startResponder(t, true, nil)
	if got, err := query(t, empty.Addr); err != nil || got != (Info{}) {
		t.Fatalf("a nil info: %+v, %v", got, err)
	}
}

// TestParseInfo reads answers built by hand the way Steam servers build
// them: every kind of extra data, The Ship's extra fields, and an old
// server's answer with no extra data at all.
func TestParseInfo(t *testing.T) {
	text := func(s string) []byte { return append([]byte(s), 0) }
	core := func(shortID uint16) []byte {
		var b []byte
		b = append(b, 17)
		b = append(b, text("Name")...)
		b = append(b, text("Map")...)
		b = append(b, text("folder")...)
		b = append(b, text("Game")...)
		b = binary.LittleEndian.AppendUint16(b, shortID)
		b = append(b, 5, 10, 1, 'd', 'w', 1, 0)
		return b
	}
	want := Info{Name: "Name", Map: "Map", Folder: "folder", Game: "Game", AppID: 892970, Players: 5, MaxPlayers: 10, Bots: 1, Version: "0.220.5"}

	// Every extra field, as Valheim's answer carries most of them. Its
	// short id is the low 16 bits of 892970.
	b := core(uint16(892970 & 0xFFFF))
	b = append(b, text("0.220.5")...)
	b = append(b, 0x80|0x10|0x40|0x20|0x01)
	b = binary.LittleEndian.AppendUint16(b, 2456)
	b = binary.LittleEndian.AppendUint64(b, 90071992547409920)
	b = binary.LittleEndian.AppendUint16(b, 27020)
	b = append(b, text("spectators")...)
	b = append(b, text("g=1,pw")...)
	b = binary.LittleEndian.AppendUint64(b, 892970)
	if got, err := parseInfo(b); err != nil || got != want {
		t.Errorf("with every extra field: %+v, %v", got, err)
	}

	// Cut anywhere before the version ends: an error. Anywhere after it:
	// the answer, with the app id from the short field until the game id
	// is whole.
	versionEnd := len(core(0)) + len(text("0.220.5"))
	for n := range len(b) {
		got, err := parseInfo(b[:n])
		switch {
		case n < versionEnd:
			if err == nil {
				t.Fatalf("cut to %d bytes: no error", n)
			}
		case err != nil:
			t.Fatalf("cut to %d bytes, after the version: %v", n, err)
		case got.Version != "0.220.5" || got.Players != 5:
			t.Fatalf("cut to %d bytes: %+v", n, got)
		}
	}

	// An old server: nothing after the version.
	old := append(core(500), text("1.0")...)
	if got, err := parseInfo(old); err != nil || got.AppID != 500 || got.Version != "1.0" {
		t.Errorf("an old server's answer: %+v, %v", got, err)
	}

	// The Ship (app id 2400) has three more bytes before the version.
	ship := append(core(2400), 1, 3, 60)
	ship = append(ship, text("1.0.0.4")...)
	if got, err := parseInfo(ship); err != nil || got.AppID != 2400 || got.Version != "1.0.0.4" {
		t.Errorf("The Ship's answer: %+v, %v", got, err)
	}
}

func FuzzParseInfo(f *testing.F) {
	f.Add(encodeInfo(enshrouded)[5:])
	f.Add(encodeInfo(Info{AppID: 2400})[5:])
	f.Add([]byte{17, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		parseInfo(b) // must never panic
	})
}
