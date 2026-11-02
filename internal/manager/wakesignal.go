package manager

// Wake signals: after someone types /valheim in Discord, the waker on the
// JetKVM sends Wake-on-LAN packets for a few minutes. If this computer is
// asleep, its network adapter wakes it up; once it's awake, the app hears
// them here and starts the server.

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
	// cutting it short. A wake signal is in the first 108 bytes.
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
			m.wakeSignal(string(tag))
		}
	}
}

// wakeSignal starts the server, unless it's running or busy, and keeps the
// computer awake for a while: someone wants to play. The waker repeats its
// signal for a few minutes with the same tag, and each request starts the
// server once: if someone stops it on purpose meanwhile, it stays stopped.
// A request heard while the server can't start yet (a job is in progress,
// or it's stopping) is kept, so a repeat starts it once it can. A signal
// without a tag (from the KVM's own Wake-on-LAN button) starts it at most
// once a minute.
func (m *Manager) wakeSignal(tag string) {
	m.mu.Lock()
	m.idleSince = time.Now() // someone wants to play
	fresh := tag != m.wakeTag
	if tag == "" {
		fresh = time.Since(m.wakeStart) > time.Minute
	}
	busy := m.job != nil && m.job.Finished == nil
	start := fresh && m.run == nil && !busy && m.installedLocked()
	steady := m.run != nil && !m.run.stopping && !busy // the server is already up
	if tag != "" && (start || steady) {
		m.wakeTag = tag // the request is met: its repeats do nothing
	}
	if start {
		m.resumeOnWake = false // this request starts it
		if tag == "" {
			m.wakeStart = time.Now()
		}
	}
	m.mu.Unlock()
	m.holdAwake()
	if !start {
		return
	}
	m.addActivity("info", "", "Starting the server: someone asked for it from Discord (/valheim) or with Wake-on-LAN.")
	if err := m.StartServer(); err != nil {
		m.log.Printf("start on a wake signal: %v", err)
	}
}
