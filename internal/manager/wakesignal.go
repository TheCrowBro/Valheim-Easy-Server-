package manager

// Wake signals: after someone types a game's command (/valheim,
// /minecraft…) in Discord, the waker on the JetKVM sends Wake-on-LAN
// packets for a few minutes, each naming that game after the request's tag
// (spec 6.4). If this computer is asleep, its network adapter wakes it up;
// once it's awake, the app hears them here and starts that game, and only
// that one. A packet that names no game comes from an older waker (the
// legacy game, as always) or, without a tag, from the KVM's own
// Wake-on-LAN button (what ran before the computer slept, 4.7).

import (
	"errors"
	"fmt"
	"net"
	"time"

	"valheim-easy-server/internal/wol"
)

// syncWakeListener listens for wake signals while wake is on and the app
// runs, and stops listening otherwise.
func (m *Manager) syncWakeListener() {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := m.settings.DiscordWake && m.ctx != nil && m.ctx.Err() == nil
	if want == (m.wakeConn != nil) {
		return
	}
	if !want {
		m.wakeConn.Close()
		m.wakeConn = nil
		return
	}
	port := m.opt.WakePort
	if port == 0 {
		port = wol.Port
	}
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		m.log.Printf("can't listen for wake signals: %v", err)
		return
	}
	m.wakeConn = conn
	go m.hearWakeSignals(conn)
}

func (m *Manager) hearWakeSignals(conn net.PacketConn) {
	defer m.recoverPanic()
	// Room for any packet: Windows fails a read that doesn't fit rather than
	// cutting it short. A wake signal is in the first 108 bytes, and the
	// game it names follows them.
	buf := make([]byte, 64<<10)
	var complained time.Time
	for {
		n, _, err := conn.ReadFrom(buf)
		if errors.Is(err, net.ErrClosed) {
			return // stopped listening
		}
		if err != nil {
			// Something odd arrived, or the network changed: keep listening,
			// or no wake signal would be heard until the app restarts.
			if time.Since(complained) > time.Hour {
				complained = time.Now()
				m.log.Printf("listening for wake signals: %v", err)
			}
			time.Sleep(100 * time.Millisecond) // don't spin if it keeps failing
			continue
		}
		if mac, tag, ok := wol.Target(buf[:n]); ok && wol.Mine(mac) {
			// A newer waker names the game it's for (spec 6.4); a packet
			// without one is for the legacy game, as always.
			if id := wakePacketGame(buf[:n]); id != "" && len(tag) > 0 {
				m.wakeGame(id, string(tag))
			} else {
				m.wakeSignal(string(tag))
			}
		}
	}
}

// wakePacketGame is the game a wake packet asks for: the instance id a
// newer waker puts after the tag, or "" for a packet without one (an older
// waker, or the KVM's own Wake-on-LAN button). It is the one place the
// app reads it; a packet for a game this copy doesn't run (or one this
// version can't read) is left for a copy of the app that runs it. Tests
// replace it.
var wakePacketGame = wol.GameOf

// startFromRequest starts a game for a Discord request: stream A's
// startFromWake (resume.go), which starts only that game and cancels a
// resume still to come. Tests replace it.
var startFromRequest = func(m *Manager, id, tag string) error {
	err := m.startFromWake(id, tag)
	if errors.Is(err, errNotYet) {
		// Stream A's start isn't built into this copy yet: the legacy game
		// starts the way it always did from a wake signal, so /valheim
		// keeps working meanwhile.
		if inst, _ := m.instanceByID(id); inst != nil && inst.legacy {
			return m.StartServer()
		}
	}
	return err
}

// wakeRepeats is how long after a request's first packet its repeats may
// still come: the waker sends one every 10 s for 3 minutes. A request kept
// while its game was busy gets one last try after it. Tests shorten it.
var wakeRepeats = 3*time.Minute + 15*time.Second

// errStillStopping is why a request that came while its game was stopping
// didn't start it, when the game still hadn't stopped after the repeats.
var errStillStopping = errors.New("It was still saving and shutting down. Ask again in a minute.")

// wakeSignal handles a wake packet that names no game. With a tag, it is a
// Discord request from an older waker, which only knew the legacy game:
// that game starts, as always. Without one it is the KVM's own Wake-on-LAN
// button (or another device): the games that ran before the computer slept
// start again, if they haven't yet (stream A's resumeNow); otherwise it
// only keeps the computer awake (decided: the button starts what ran
// before; if nothing did, it just wakes).
func (m *Manager) wakeSignal(tag string) {
	if tag != "" {
		m.wakeGame(m.legacyID, tag)
		return
	}
	m.mu.Lock()
	// Someone wants to play. The observers (hooks.go) hear of it too.
	m.machineIdleSince = time.Now()
	m.observeLocked(func(o observer) { o.onWake(wakeUntagged) })
	m.mu.Unlock()
	m.holdAwake()
	m.resumeNow()
}

// wakeGame handles a Discord request's wake packet for the instance id. It
// starts that game only, and only once per request: the waker repeats its
// packet with the same tag for a few minutes, so if someone stops the game
// on purpose meanwhile, it stays stopped. A request heard while the game
// can't start yet (a job of its own, or it's stopping) is kept, so a repeat
// starts it once it can; when the repeats are over the app tries once more
// and says in the game's channel why not, if it still can't. Any other
// refusal (memory short, turned off, not installed) is answered there at
// once (wakeReply).
func (m *Manager) wakeGame(id, tag string) {
	inst, err := m.instanceByID(id)
	if err != nil || inst.srv == nil || inst.foreign != "" {
		// A newer copy's game, or one removed since: the copy that runs it
		// starts it (after a rollback, this one reinstalls its own waker
		// within 90 s of starting).
		m.log.Printf("a wake signal asked for %q, which this copy of the app doesn't run", id)
		return
	}
	now := time.Now()
	m.mu.Lock()
	// Someone wants to play, and asked from Discord: the resume after a
	// wake leaves the games alone, as this request starts only its own
	// (4.7). The observers (hooks.go) hear of it too.
	m.machineIdleSince = now
	m.wakeAsked = now
	m.observeLocked(func(o observer) { o.onWake(wakeTagged) })
	m.cancelResumeLocked()
	if m.wakeTags == nil {
		m.wakeTags = map[string]string{}
	}
	done := m.wakeTags[id] == tag // met or answered already: its repeats do nothing
	if m.isHiddenLocked(inst) {
		// Removed from the panel (I1): nothing starts it, and it has no
		// command; an older waker's /valheim may still ask.
		m.mu.Unlock()
		m.log.Printf("a wake signal asked for %s, which is removed from the panel", inst.short())
		return
	}
	if !done && m.isDisabledLocked(inst) {
		// Turned off (8.3): it has no command either, but a request from
		// before that, or from an older waker, is told why.
		m.wakeTags[id] = tag
		m.mu.Unlock()
		m.holdAwake()
		m.wakeReply(inst, fmt.Errorf("%s is turned off on its Settings tab.", inst.short()))
		return
	}
	run := inst.run
	busy := m.busyJobLocked(inst) != nil
	steady := run != nil && !run.stopping && !busy // it's already up, or on its way up
	var post discordPost
	var online bool
	if !done && steady {
		m.wakeTags[id] = tag // met: it's up, or on its way up
		online = m.metLocked(inst, &post)
	}
	start := !done && run == nil && !busy
	keep := !done && !start && !steady
	m.mu.Unlock()
	m.holdAwake()
	switch {
	case online:
		m.queueDiscord(post)
	case keep:
		m.keepRequest(inst, tag)
	case start:
		m.startForRequest(inst, tag, false)
	}
}

// metLocked notes a request met by a game that is already running: when
// it's online, post says so in its channel (online reports that); while
// it's starting, its online post will tell the channel once it's ready.
func (m *Manager) metLocked(inst *instance, post *discordPost) (online bool) {
	if run := inst.run; run != nil && run.ready {
		if *post, online = m.alreadyOnlinePostLocked(inst, run); online {
			return true
		}
	}
	m.askedLocked(inst)
	return false
}

// askedLocked notes that a Discord request asked for the instance's
// current start or run.
func (m *Manager) askedLocked(inst *instance) {
	if m.wk.asked == nil {
		m.wk.asked = map[string]bool{}
	}
	m.wk.asked[inst.id] = true
}

// startForRequest starts inst for the request tag. last is the try after
// the waker's repeats are over: a game that is still busy is then answered
// rather than kept.
func (m *Manager) startForRequest(inst *instance, tag string, last bool) {
	m.mu.Lock()
	stopping := inst.run != nil && inst.run.stopping
	before := inst.job // to tell the start job this makes from an older one
	m.mu.Unlock()
	var err error
	if stopping {
		err = errStillStopping
	} else {
		err = startFromRequest(m, inst.id, tag)
	}
	var be *BusyError
	var post discordPost
	var failed error // the start this made has failed already
	online := false
	m.mu.Lock()
	if m.wakeTags == nil {
		m.wakeTags = map[string]string{}
	}
	switch {
	case err == nil:
		m.wakeTags[inst.id] = tag
		if j := inst.job; j != before && j != nil && j.Kind == "start" && j.Finished != nil && inst.run == nil {
			// The start job this made ended before it could be marked as
			// asked for from Discord (it can fail within a moment, a port
			// in use, say), so onJob didn't answer it: answer it here.
			if j.Error != "" {
				failed = errors.New(j.Error)
			}
			break
		}
		m.askedLocked(inst)
	case !errors.Is(err, errStillStopping) && (inst.run != nil && !inst.run.stopping || running(inst.job) && inst.job.Kind == "start"):
		// It was started meanwhile (a repeat got there first, or someone
		// pressed Start): the request is met all the same.
		m.wakeTags[inst.id] = tag
		online = m.metLocked(inst, &post)
		m.mu.Unlock()
		if online {
			m.queueDiscord(post)
		}
		return
	case !last && (errors.As(err, &be) || errors.Is(err, errStillStopping)):
		m.mu.Unlock()
		m.keepRequest(inst, tag)
		return
	default:
		m.wakeTags[inst.id] = tag // answered: its repeats do nothing more
	}
	m.mu.Unlock()
	if err != nil {
		m.wakeReply(inst, err)
		return
	}
	text := fmt.Sprintf("Starting %s: someone asked for it from Discord (/%s).", inst.short(), commandOf(inst))
	if inst.legacy {
		text = "Starting the server: someone asked for it from Discord (/valheim) or with Wake-on-LAN." // as always
	}
	m.addGameActivity(activityGame(inst), "info", "", text)
	if failed != nil {
		m.wakeReply(inst, failed)
	}
}

// commandOf is the game's slash command: its own, or its id.
func commandOf(inst *instance) string {
	if inst.info.Command != "" {
		return inst.info.Command
	}
	return inst.info.ID
}

// keepRequest keeps a request that came while its game couldn't start yet.
// The waker's repeats may still start it; once they're over, if none has,
// the app tries once more and answers in the game's channel if it still
// can't start (startForRequest with last).
func (m *Manager) keepRequest(inst *instance, tag string) {
	m.mu.Lock()
	if m.wk.kept == nil {
		m.wk.kept = map[string]string{}
	}
	armed := m.wk.kept[inst.id] == tag
	m.wk.kept[inst.id] = tag
	ctx := m.ctx
	m.mu.Unlock()
	if armed || ctx == nil {
		return
	}
	go func() {
		defer m.recoverPanic()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wakeRepeats):
		}
		m.mu.Lock()
		open := m.wk.kept[inst.id] == tag && m.wakeTags[inst.id] != tag
		if m.wk.kept[inst.id] == tag {
			delete(m.wk.kept, inst.id)
		}
		m.mu.Unlock()
		if open && ctx.Err() == nil {
			m.startForRequest(inst, tag, true)
		}
	}()
}
