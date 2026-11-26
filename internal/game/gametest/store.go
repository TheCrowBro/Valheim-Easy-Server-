package gametest

import (
	"sync"

	"valheim-easy-server/internal/game"
)

// memStore is the instance's settings, kept in memory: what servers.json
// keeps for the app (Env.Store).
type memStore struct {
	mu    sync.Mutex
	saved game.Saved
}

func (m *memStore) Get() game.Saved {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved.Clone()
}

// Save applies change to a copy and keeps it only when change succeeds,
// so a failed change leaves the settings as they were.
func (m *memStore) Save(change func(*game.Saved) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.saved.Clone()
	if err := change(&c); err != nil {
		return err
	}
	m.saved = c
	return nil
}
