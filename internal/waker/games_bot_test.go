package waker

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"valheim-easy-server/internal/wol"
)

// commandIn has someone type /name in a channel of a Discord server.
func (f *fakeDiscord) commandIn(guild, channel, id, nick, name string) {
	f.events <- map[string]any{"op": 0, "s": 5, "t": "INTERACTION_CREATE", "d": map[string]any{
		"id": id, "token": "tok-" + id, "type": 2, "application_id": "app1", "guild_id": guild, "channel_id": channel,
		"data":   map[string]any{"name": name},
		"member": map[string]any{"nick": nick, "user": map[string]any{"username": "x"}},
	}}
}

// gamesBot is a bot set up by an R3 app: Valheim (which answers queries)
// in channel c1, Minecraft in channel c2.
func gamesBot(t *testing.T, f *fakeDiscord, games ...GameCommand) *Bot {
	b := testBot(t, f, "secret")
	b.Config.Query, b.Config.Name = "", "" // R3 apps leave them empty
	if games == nil {
		games = []GameCommand{
			{ID: "valheim", Command: "valheim", Description: "Wake the server computer and start Valheim", Name: "Viking Night", Channel: "c1", Query: "192.0.2.20:2457"},
			{ID: "minecraft", Command: "minecraft", Description: "Wake the server computer and start Minecraft", Name: "Friends' Minecraft", Channel: "c2"},
		}
	}
	b.Config.Games = &games
	return b
}

// packets records the wake packets the bot sends, by the game they name.
type packets struct {
	mu   sync.Mutex
	by   map[string]int
	tags map[string]map[string]bool
}

func (p *packets) send(mac net.HardwareAddr, tag []byte, id string, targets []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.by == nil {
		p.by, p.tags = map[string]int{}, map[string]map[string]bool{}
	}
	if p.tags[id] == nil {
		p.tags[id] = map[string]bool{}
	}
	p.by[id]++
	p.tags[id][string(tag)] = true
	return nil
}

func (p *packets) count(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.by[id]
}

func (p *packets) requests(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tags[id])
}

// An R3 app's settings: the bot registers exactly the commands in Games,
// each works only in its game's channel, and each game wakes on its own.
func TestGameCommands(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	f.commands["g1"] = []map[string]any{command} // an older waker's /valheim
	b := gamesBot(t, f)
	b.Config.Guild = "g1" // the main channel's Discord server
	sent := &packets{}
	b.send = sent.send
	var up atomic.Bool
	b.query = func(ctx context.Context, addr string) (Info, error) {
		if addr != "192.0.2.20:2457" || !up.Load() {
			return Info{}, errors.New("no answer")
		}
		return Info{Name: "Viking Night", Players: 2}, nil
	}
	run(t, b)
	f.wait(t, "the two commands", func() bool {
		c := f.commands["g1"]
		return len(c) == 2 && c[0]["name"] == "valheim" && c[1]["name"] == "minecraft" &&
			c[1]["description"] == "Wake the server computer and start Minecraft" && c[1]["type"] == float64(1)
	})

	// In another game's channel: only the one who asked is told where.
	f.commandIn("g1", "c1", "i1", "Astrid", "minecraft")
	f.wait(t, "the refusal", func() bool { return has(f.replies, "(only them) 🔒 /minecraft works in <#c2>.") })
	time.Sleep(100 * time.Millisecond)
	if sent.count("minecraft") != 0 {
		t.Fatal("woke the computer for a command typed in the wrong channel")
	}

	// In its own channel: the computer is woken for Minecraft.
	f.commandIn("g1", "c2", "i2", "Astrid", "minecraft")
	f.wait(t, "the answer", func() bool {
		return has(f.replies, "⏰ Waking the server computer and starting **Friends' Minecraft**. It posts here when it's online.")
	})
	waitN(t, "Minecraft's wake packets", func() bool { return sent.count("minecraft") >= 2 })

	// Each game wakes on its own: /valheim while Minecraft is being woken
	// wakes the computer for Valheim too, with its own request.
	f.commandIn("g1", "c1", "i3", "Bjorn", "valheim")
	f.wait(t, "Valheim's answer", func() bool {
		return has(f.replies, "⏰ Waking the server computer and starting **Viking Night**.")
	})
	waitN(t, "Valheim's wake packets", func() bool { return sent.count("valheim") >= 2 })
	// The same game again: it's already on its way.
	f.commandIn("g1", "c2", "i4", "Sigrid", "minecraft")
	f.wait(t, "the second answer", func() bool {
		return has(f.replies, "⏰ **Friends' Minecraft** is already being started: Astrid asked just now.")
	})
	if sent.requests("minecraft") != 1 || sent.requests("valheim") != 1 || sent.count("") != 0 {
		t.Fatalf("requests: %d for Minecraft, %d for Valheim, %d naming no game", sent.requests("minecraft"), sent.requests("valheim"), sent.count(""))
	}

	// Minecraft doesn't answer queries: the bot stops after its few
	// minutes of packets and leaves the rest to the app's posts.
	waitN(t, "Minecraft's wake to end", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, on := b.waking["minecraft"]
		return !on
	})
	n := sent.count("minecraft")
	time.Sleep(100 * time.Millisecond)
	if sent.count("minecraft") != n {
		t.Fatal("still sending Minecraft's wake packets")
	}
	// Valheim does: its answer changes once it's up.
	up.Store(true)
	f.wait(t, "Valheim's update", func() bool {
		return has(f.edits, "tok-i3: ✅ **Viking Night** is online, with 2 playing. Have fun!")
	})
	f.mu.Lock()
	for _, e := range f.edits {
		if strings.HasPrefix(e, "tok-i2:") {
			t.Errorf("the bot claimed something about Minecraft: %q", e)
		}
	}
	f.mu.Unlock()

	// Already up: nothing to wake.
	f.commandIn("g1", "c1", "i5", "Sigrid", "valheim")
	f.wait(t, "the answer when online", func() bool { return has(f.replies, "🟢 **Viking Night** is already online, with 2 playing.") })
	if sent.requests("valheim") != 1 {
		t.Fatal("woke a server that was up")
	}

	// Another Discord server than its own is refused, and a command the bot
	// doesn't have (an older one, not taken away yet) is left alone.
	f.commandIn("g2", "c2", "i6", "Loki", "minecraft")
	f.wait(t, "the refusal in another server", func() bool {
		return has(f.replies, "(only them) "+wrongServerText)
	})
	f.commandIn("g1", "c2", "i7", "Loki", "terraria")
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replies) != 6 {
		t.Fatalf("replies: %q", f.replies)
	}
}

func waitN(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Nothing starred: the bot takes every command away, /valheim included
// (an empty bulk overwrite), and still counts the Discord server as set up.
func TestNoGameCommands(t *testing.T) {
	f := newFakeDiscord(t, "secret")
	f.commands["g1"] = []map[string]any{command}
	none := []GameCommand{}
	b := testBot(t, f, "secret")
	b.Config.Games = &none
	run(t, b)
	f.wait(t, "the commands to go", func() bool {
		c, ok := f.commands["g1"]
		return ok && c != nil && len(c) == 0
	})
	f.interaction("i1", "Astrid") // a /valheim Discord still showed
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	replies := len(f.replies)
	f.mu.Unlock()
	if replies != 0 {
		t.Fatal("answered a command it doesn't have")
	}
	waitN(t, "the status", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.status.Servers == 1
	})
}

// Entries Discord wouldn't take, or that couldn't work, are left out; the
// others are still set up. Descriptions are never empty or too long.
func TestGameCommandsChecked(t *testing.T) {
	long := strings.Repeat("Wake ", 30)
	games := []GameCommand{
		{ID: "minecraft", Command: "minecraft", Description: long, Name: "Blocks", Channel: "c2"},
		{ID: "terraria", Command: "Terraria", Name: "Caves", Channel: "c3"},         // a capital
		{ID: "necesse", Command: "minecraft", Name: "Again", Channel: "c4"},         // a second /minecraft
		{ID: "palworld", Command: "palworld", Name: "Pals", Channel: ""},            // no channel
		{ID: "", Command: "zomboid", Name: "Zeds", Channel: "c5"},                   // no game
		{ID: "corekeeper", Command: "corekeeper", Name: "Core Crew", Channel: "c6"}, // no description
	}
	cmds, skipped := Config{Games: &games}.commands()
	if len(cmds) != 2 || cmds[0].Command != "minecraft" || cmds[1].Command != "corekeeper" || len(skipped) != 4 {
		t.Fatalf("commands %+v, skipped %q", cmds, skipped)
	}
	if d := cmds[0].Description; len([]rune(d)) != maxDescription || !strings.HasSuffix(d, "…") {
		t.Errorf("a long description became %q", d)
	}
	if d := cmds[1].Description; d != "Wake the server computer and start Core Crew" {
		t.Errorf("an empty description became %q", d)
	}
	// An older app's settings: /valheim from Query and Name, as always.
	old, skipped := Config{Query: "192.0.2.20:2457", Name: "Viking Night"}.commands()
	if len(old) != 1 || !old[0].legacy || old[0].Command != "valheim" || old[0].ID != "" || old[0].Query != "192.0.2.20:2457" ||
		old[0].Name != "Viking Night" || skipped != nil {
		t.Fatalf("an older app's command: %+v", old)
	}
	if label(cmds) != "/minecraft and /corekeeper" || label(nil) != "its commands" || label(old) != "/valheim" {
		t.Errorf("labels: %q %q %q", label(cmds), label(nil), label(old))
	}
}

// A game that answers queries and still doesn't after the wait: the answer
// says so, and points to the app's own post in the channel.
func TestGameCommandTimesOut(t *testing.T) {
	want := "⚠️ **Valheim** didn't answer within 10 minutes. If the app couldn't start it, it says why above; otherwise someone may need to look at it through the KVM."
	if got := timeoutText(slash{GameCommand: GameCommand{Command: "valheim", Name: "Valheim"}}, minutes(10*time.Minute)); got != want {
		t.Fatalf("the 10-minute text = %q", got)
	}
	f := newFakeDiscord(t, "secret")
	b := gamesBot(t, f)
	b.waitFor = 200 * time.Millisecond
	b.send = func(net.HardwareAddr, []byte, string, []string) error { return nil }
	b.query = func(context.Context, string) (Info, error) { return Info{}, errors.New("no answer") }
	run(t, b)
	f.wait(t, "the commands", func() bool { return len(f.commands["g1"]) == 2 })
	f.commandIn("g1", "c1", "i1", "Astrid", "valheim")
	f.wait(t, "the update", func() bool {
		return has(f.edits, "tok-i1: ⚠️ **Viking Night** didn't answer within a minute. If the app couldn't start it, it says why above")
	})
}

// The packets a game's command sends name that game after the request's
// tag, so the app starts only it; the tag stays the same for the request.
func TestGameWakePackets(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	f := newFakeDiscord(t, "secret")
	b := gamesBot(t, f)
	b.Config.Targets = []string{conn.LocalAddr().String()}
	b.query = func(context.Context, string) (Info, error) { return Info{}, errors.New("no answer") }
	run(t, b)
	f.wait(t, "the commands", func() bool { return len(f.commands["g1"]) == 2 })
	f.commandIn("g1", "c2", "i1", "Astrid", "minecraft")
	buf := make([]byte, 200)
	var tag []byte
	for i := range 2 {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		mac, got, ok := wol.Target(buf[:n])
		if !ok || mac.String() != "3c:07:54:5a:21:9f" || len(got) != 6 || wol.GameOf(buf[:n]) != "minecraft" {
			t.Fatalf("packet %d = %x", i, buf[:n])
		}
		if tag != nil && string(tag) != string(got) {
			t.Fatalf("the tag changed from %x to %x within one request", tag, got)
		}
		tag = got
	}
}
