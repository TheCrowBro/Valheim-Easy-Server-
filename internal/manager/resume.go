package manager

// Waking up and the resume (spec 4.7, stream A). Going to sleep records the
// games that were running (resumeSet); waking on purpose waits until the
// network is really back, then 30 s more, and starts them again, unless a
// Discord request was heard since the wake, which starts only its own game
// (startFromWake). The KVM's own Wake-on-LAN button (an untagged packet)
// resumes at once.
//
// The resume waits for the network because a game started before the
// network adapter is up again can fail to register with Steam or bind its
// address, and friends would find it "online" but unreachable; and because
// a Discord request that woke the computer is repeated every 10 s for 3
// minutes, so waiting gives it the chance to be heard and to have its
// way: someone asked for one game, so only that one starts.
//
// Stream WK's wakesignal.go calls startFromWake and resumeNow; A's own
// observer (autostop.go) also hears every packet, so a tagged one calls the
// resume off and an untagged one resumes, whichever code sent it.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"syscall"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// The resume's timings and its look at the network. Replaced in tests,
// which run them on a fake clock.
var (
	resumeSettle   = 30 * time.Second // after the network is back, before the first game starts
	networkEvery   = 2 * time.Second  // how often the network is looked at
	networkMax     = 3 * time.Minute  // the longest the resume waits for it
	gatewayAnswers = defaultGatewayAnswers
)

// resumeRun is one resume after waking: waiting for the network, or
// starting the games one after another. Manager.life.resume holds the one
// in progress, guarded by m.mu.
type resumeRun struct {
	ids    []string           // the games to start, in order
	ctx    context.Context    // cancelled when a Discord request calls it off
	cancel context.CancelFunc //
	// skip ends the waiting at once (an untagged packet: the network works,
	// since the packet came through it, and someone asked).
	skip     context.Context
	skipNow  context.CancelFunc
	starting bool // it has begun starting the games
	next     int  // how many of ids it has taken (started, or found it couldn't)
	held     bool // called off because the computer went to sleep (holdResumeLocked)
}

// startFromWake starts the game a Discord request's wake packet asks for
// (id, the instance; tag, the request): only that game, once per request,
// cancelling any pending resume and noting wakeAsked. It runs without m.mu.
// An error says why it can't start (memory short, busy, turned off, not
// installed); WK's wakesignal.go then posts that in the game's channel
// (wakeReply, waker.go).
//
// The waker repeats a request's packet every 10 s for 3 minutes, so: a
// request is met once its game starts or is found running, and its
// repeats then answer nil and do nothing (if someone stops the game on
// purpose meanwhile, it stays stopped); a request refused for good (memory,
// turned off, removed, not installed) answers its error once, and nil for
// the repeats, so the channel gets one reply; a request that finds the game
// busy (a job, or it is stopping) answers a *BusyError and stays open, so a
// repeat starts the game once it can.
func (m *Manager) startFromWake(id, tag string) error {
	inst, err := m.instanceByID(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	now := m.clock().Now()
	m.wakeAsked, m.life.taggedAt = now, now
	m.cancelResumeLocked() // a Discord request starts only its own game
	if m.wakeTags == nil {
		m.wakeTags = map[string]string{}
	}
	met := func() {
		if tag != "" {
			m.wakeTags[id] = tag
		}
	}
	if tag != "" && m.wakeTags[id] == tag {
		m.mu.Unlock()
		return nil // this request was met already
	}
	if err := m.cantStartLocked(inst); err != nil {
		met()
		m.mu.Unlock()
		return err
	}
	switch busy := m.busyJobLocked(inst); {
	case inst.run != nil && inst.run.stopping:
		m.mu.Unlock()
		return &BusyError{Title: "stopping the " + inst.short() + " server"}
	case busy != nil:
		m.mu.Unlock()
		return &BusyError{Title: lowerFirst(busy.Title)}
	case inst.run != nil:
		met() // it is up already
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	err = m.startGameWith(inst, StartOptions{Source: SourceDiscord})
	var be *BusyError
	if !errors.As(err, &be) {
		m.mu.Lock()
		met()
		m.mu.Unlock()
	}
	return err
}

// cantStartLocked says why nothing but the owner, in the panel, may start
// the instance now: it is a hidden legacy game (I1), turned off on its
// Settings tab (8.3), one this build doesn't run, or can't run on this
// computer. nil when none of these hold.
func (m *Manager) cantStartLocked(inst *instance) error {
	if err := m.hiddenRefusalLocked(inst); err != nil {
		return err
	}
	switch {
	case inst.srv == nil:
		why := inst.foreign
		if why == "" {
			why = "this version of the app doesn't run it"
		}
		return fmt.Errorf("%s can't be started: %s.", inst.short(), strings.TrimSuffix(why, "."))
	case m.isDisabledLocked(inst):
		return fmt.Errorf("%s is turned off on its Settings tab.", inst.short())
	}
	if !inst.legacy { // the legacy game has always run wherever the app does
		if l, why := m.canRunLocked(inst); l == game.CantRun {
			if why == "" {
				why = "It can't run on this computer"
			}
			return fmt.Errorf("%s can't start here: %s.", inst.short(), strings.TrimSuffix(why, "."))
		}
	}
	return nil
}

// hiddenRefusalLocked refuses a start of the legacy game while it is
// removed from the panel (invariant I1: a hidden Valheim never runs
// unseen), with the words spec 4.3 gives.
func (m *Manager) hiddenRefusalLocked(inst *instance) error {
	if m.isHiddenLocked(inst) {
		return errors.New(inst.short() + " is removed from the panel. Add it again first.")
	}
	return nil
}

// scheduleResume starts the resume set once the network is back and 30 s
// more have passed, unless a Discord request came first. wokenOnPurpose
// (power.go) calls it. With "start again after waking" off, or a Discord
// request heard since the wake, the games that were running stay off and
// are forgotten, as the single "start again" of earlier versions was.
func (m *Manager) scheduleResume() {
	m.mu.Lock()
	switch {
	case len(m.resumeSet) == 0, m.life.resume != nil:
		m.mu.Unlock()
		return
	case !m.settings.StartAfterWake || m.taggedSinceWakeLocked():
		m.resumeSet = nil
		m.mu.Unlock()
		return
	}
	r := m.newResumeLocked()
	names := m.resumeNamesLocked(r.ids)
	m.mu.Unlock()
	if len(r.ids) == 1 && r.ids[0] == m.legacyID {
		// Valheim's activity texts say "the server", as they always did.
		m.addActivity("info", "", "Starting the server again once the network is back (it was running before the computer went to sleep).")
	} else {
		m.addActivity("info", "", fmt.Sprintf("Starting %s again once the network is back (%s running before the computer went to sleep).", names, itWas(len(r.ids))))
	}
	m.go_(func() { m.runResume(r) })
}

// itWas is "it was" for one game and "they were" for several.
func itWas(n int) string {
	if n == 1 {
		return "it was"
	}
	return "they were"
}

// resumeNow starts the resume set at once, if it hasn't been yet (an
// untagged wake packet; WK's wakesignal.go calls it, and so does A's
// observer). A resume already waiting for the network goes ahead now.
func (m *Manager) resumeNow() {
	m.mu.Lock()
	if r := m.life.resume; r != nil {
		r.skipNow()
		m.mu.Unlock()
		return
	}
	if len(m.resumeSet) == 0 {
		m.mu.Unlock()
		return
	}
	r := m.newResumeLocked()
	r.skipNow()
	names := m.resumeNamesLocked(r.ids)
	m.mu.Unlock()
	m.addActivity("info", "", fmt.Sprintf("Starting %s again: someone asked with Wake-on-LAN, and %s running before the computer went to sleep.",
		names, itWas(len(r.ids))))
	m.go_(func() { m.runResume(r) })
}

// newResumeLocked makes the resume of the resume set and makes it the one
// in progress.
func (m *Manager) newResumeLocked() *resumeRun {
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	r := &resumeRun{ids: append([]string(nil), m.resumeSet...)}
	r.ctx, r.cancel = context.WithCancel(parent)
	r.skip, r.skipNow = context.WithCancel(r.ctx)
	m.life.resume = r
	return r
}

// resumeNamesLocked names the games for people: "Valheim", "Valheim and
// Minecraft", "Valheim, Minecraft and Terraria".
func (m *Manager) resumeNamesLocked(ids []string) string {
	var names []string
	for _, id := range ids {
		if inst := m.instances[id]; inst != nil {
			names = append(names, inst.short())
		} else {
			names = append(names, id)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// runResume waits for the network and the 30 s after it (unless told to
// go now), then starts each game of the resume in order. Each start waits
// for the one before it to finish, so its memory check sees the memory the
// game before it took, and a Discord request heard meanwhile can still
// keep the rest off.
func (m *Manager) runResume(r *resumeRun) {
	defer func() {
		m.mu.Lock()
		if m.life.resume == r {
			m.life.resume = nil
		}
		m.mu.Unlock()
		r.cancel()
	}()
	clk := m.clock()
	if r.skip.Err() == nil {
		if !m.networkBack(r.skip) && r.skip.Err() == nil {
			m.log.Printf("resume after waking: the network didn't come back within %s; starting the games anyway", networkMax)
		}
		select {
		case <-clk.After(resumeSettle):
		case <-r.skip.Done():
		}
	}
	m.mu.Lock()
	if r.ctx.Err() != nil || m.life.resume != r {
		m.mu.Unlock()
		return // called off by a Discord request
	}
	r.starting = true
	m.resumeSet = nil // taken: going to sleep again records afresh
	m.mu.Unlock()
	for i, id := range r.ids {
		inst, err := m.instanceByID(id)
		m.mu.Lock()
		if r.ctx.Err() != nil {
			// Called off: by a Discord request, and the games not started
			// yet stay off; or by going to sleep, and they stay owed
			// (holdResumeLocked put them back).
			m.mu.Unlock()
			return
		}
		r.next = i + 1 // taken: a sleep from now on leaves it to this loop
		if err != nil {
			m.mu.Unlock()
			continue // removed meanwhile
		}
		up := inst.run != nil
		why := m.cantStartLocked(inst)
		m.mu.Unlock()
		if up {
			continue // someone started it already
		}
		if why == nil {
			why = m.startGameWith(inst, StartOptions{Source: SourceResume})
		}
		if why != nil {
			// Going to sleep began just as this start was asked for, so it
			// was refused as busy (its job is the app's): this game and the
			// ones after it stay owed for the next wake, with no warning.
			// The sleep job may not have called the resume off yet
			// (holdResumeLocked), so this puts them back itself.
			m.mu.Lock()
			held := r.held || running(m.appJob) && m.appJob.Kind == "hibernate"
			if held {
				for _, later := range r.ids[i:] {
					if !slices.Contains(m.resumeSet, later) {
						m.resumeSet = append(m.resumeSet, later)
					}
				}
				r.next = len(r.ids)
			}
			m.mu.Unlock()
			if held {
				return
			}
			m.addGameActivity(inst, "warning", "", inst.short()+" wasn't started again after waking: "+resumeWhy(why))
			continue
		}
		m.waitStartJob(r.ctx, inst)
	}
}

// resumeWhy words why a game wasn't started again, after "wasn't started
// again after waking: ".
func resumeWhy(err error) string {
	var ae *ActionError
	if errors.As(err, &ae) && ae.Action == "memory" {
		if ms, ok := ae.Data.(MemoryShort); ok {
			return fmt.Sprintf("it needs about %s of memory and %s is free.", sizeMB(ms.NeedMB), sizeMB(ms.FreeMB))
		}
	}
	s := err.Error()
	if s == "" {
		return "it couldn't start."
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// waitStartJob waits until the instance's start job ends (it is ready, or
// gave up), or ctx ends.
func (m *Manager) waitStartJob(ctx context.Context, inst *instance) {
	m.mu.Lock()
	job := inst.job
	m.mu.Unlock()
	if job == nil {
		return
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		m.mu.Lock()
		done := job.Finished != nil
		m.mu.Unlock()
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// holdResumeLocked calls off the resume in progress, if any, because the
// computer is going to sleep (Hibernate) or has slept since the resume
// began (onWake): the owner pressed Sleep while it waited for the
// network, say, or closed the lid. Left alone, its timers would carry on
// after the next wake of any kind (on Windows they count through sleep, on
// a Mac they pick up where they were), so a wake timer or a Mac's dark
// wake would start games nobody asked for, and those would then keep the
// computer awake for their hour; and while it was pending, no wake on
// purpose would schedule a fresh one. The games it hadn't taken yet stay
// owed (resumeSet), so the next wake on purpose starts them, after its
// own wait for the network. A Discord request calls a resume off for good
// instead (cancelResumeLocked).
func (m *Manager) holdResumeLocked() {
	r := m.life.resume
	if r == nil {
		return
	}
	r.held = true
	r.cancel()
	m.life.resume = nil
	for _, id := range r.ids[r.next:] {
		if !slices.Contains(m.resumeSet, id) {
			m.resumeSet = append(m.resumeSet, id)
		}
	}
	m.log.Printf("resume after waking: called off, the computer is resting; %d game(s) stay owed", len(m.resumeSet))
}

// cancelResumeLocked stops a resume that hasn't started its games yet; the
// ones already starting go on. The resume set is forgotten too: a Discord
// request decided what this wake is for.
func (m *Manager) cancelResumeLocked() {
	if r := m.life.resume; r != nil {
		r.cancel()
		m.life.resume = nil
	}
	m.resumeSet = nil
}

// taggedSinceWakeLocked says whether a Discord request's packet was heard
// since the computer last woke up. A packet heard a moment before the app
// noticed the wake (the listener can read it before the next sample tick
// does) counts too.
func (m *Manager) taggedSinceWakeLocked() bool {
	t := m.life.taggedAt
	return !t.IsZero() && t.After(m.life.wokeAt.Add(-m.opt.SampleInterval-time.Second))
}

// networkBack waits until the network is really back after a wake: the
// wake listener is bound again and the default gateway answers a TCP
// connection on port 53 or 80 (accepted or refused, within 1 s), checked
// every 2 s for at most 3 minutes. It reports whether it came back.
func (m *Manager) networkBack(ctx context.Context) bool {
	clk := m.clock()
	deadline := clk.Now().Add(networkMax)
	for {
		if m.wakeListening() && gatewayAnswers(ctx) {
			return true
		}
		if ctx.Err() != nil || !clk.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-clk.After(networkEvery):
		}
	}
}

// wakeListening says whether the app hears wake packets, when it should
// (the waker is set up): it tries to listen again if it doesn't, as the
// network can come back after the app last tried.
func (m *Manager) wakeListening() bool {
	m.mu.Lock()
	want, have := m.settings.DiscordWake, m.wakeConn != nil
	m.mu.Unlock()
	if !want || have {
		return true
	}
	m.syncWakeListener()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wakeConn != nil
}

// wsaeConnRefused is Windows' "connection refused" (WSAECONNREFUSED),
// which Go reports as this number rather than syscall.ECONNREFUSED. No
// other system has an error with this number, so it is safe to check
// everywhere.
const wsaeConnRefused = syscall.Errno(10061)

// defaultGatewayAnswers says whether the router answers: a TCP connection
// to it on port 53 or 80 is accepted or refused within a second. Either
// answer proves the link to it is up; no answer (a timeout), or no route
// to it, doesn't. No ICMP, which needs privileges on some systems.
func defaultGatewayAnswers(ctx context.Context) bool {
	gw, err := platform.DefaultGateway()
	if err != nil || gw == nil {
		return false
	}
	for _, port := range []string{"53", "80"} {
		d := net.Dialer{Timeout: time.Second}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(gw.String(), port))
		if err == nil {
			c.Close()
			return true
		}
		var errno syscall.Errno
		if errors.As(err, &errno) && (errno == syscall.ECONNREFUSED || errno == wsaeConnRefused) {
			return true
		}
	}
	return false
}
