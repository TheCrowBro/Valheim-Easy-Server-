package manager

// Wake signals: after someone types /valheim in Discord, the waker on the
// JetKVM sends Wake-on-LAN packets for a few minutes. If this computer is
// asleep, its network adapter wakes it up; once it's awake, the app hears
// them here and starts the server.

import (
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
	buf := make([]byte, 512)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return // stopped listening
		}
		if mac, tag, ok := wol.Target(buf[:n]); ok && wol.Mine(mac) {
			m.wakeSignal(string(tag))
		}
	}
}

// wakeSignal starts the server, unless it's running or busy. The waker
// repeats its signal for a few minutes with the same tag, and each request
// starts the server once: if someone stops it on purpose meanwhile, it stays
// stopped. A signal without a tag (from the KVM's own Wake-on-LAN button)
// starts it at most once a minute.
func (m *Manager) wakeSignal(tag string) {
	m.mu.Lock()
	m.idleSince = time.Now() // someone wants to play
	fresh := tag != m.wakeTag
	if tag == "" {
		fresh = time.Since(m.wakeStart) > time.Minute
	}
	m.wakeTag = tag
	busy := m.job != nil && m.job.Finished == nil
	start := fresh && m.run == nil && !busy && m.installedLocked()
	if start && tag == "" {
		m.wakeStart = time.Now()
	}
	m.mu.Unlock()
	if !start {
		return
	}
	m.addActivity("info", "", "Starting the server: someone asked for it from Discord (/valheim) or with Wake-on-LAN.")
	if err := m.StartServer(); err != nil {
		m.log.Printf("start on a wake signal: %v", err)
	}
}
