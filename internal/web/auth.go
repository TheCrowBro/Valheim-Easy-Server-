package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
)

const (
	cookieName    = "ves_session"
	remoteSession = 30 * 24 * time.Hour
)

type session struct {
	Local    bool      `json:"local"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
}

// sessions keeps who is logged in. Remote sessions are saved so friends
// stay logged in when the app restarts; only a hash of each ID is stored.
type sessions struct {
	mu   sync.Mutex
	file string
	byID map[string]*session // key: sha256 of the cookie value
}

func newSessions(file string) *sessions {
	s := &sessions{file: file, byID: map[string]*session{}}
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &s.byID)
	}
	for k, v := range s.byID {
		if v.Local || time.Since(v.LastSeen) > remoteSession {
			delete(s.byID, k)
		}
	}
	return s
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func (s *sessions) create(local bool) string {
	id := config.RandomToken(32)
	s.mu.Lock()
	s.byID[hashID(id)] = &session{Local: local, Created: time.Now(), LastSeen: time.Now()}
	s.mu.Unlock()
	if !local {
		s.save()
	}
	return id
}

func (s *sessions) get(id string) (session, bool) {
	if id == "" {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[hashID(id)]
	if !ok {
		return session{}, false
	}
	if !v.Local && time.Since(v.LastSeen) > remoteSession {
		delete(s.byID, hashID(id))
		return session{}, false
	}
	v.LastSeen = time.Now()
	return *v, true
}

func (s *sessions) remove(id string) {
	s.mu.Lock()
	delete(s.byID, hashID(id))
	s.mu.Unlock()
	s.save()
}

// removeRemote logs out every friend (used when the panel password changes).
func (s *sessions) removeRemote() {
	s.mu.Lock()
	for k, v := range s.byID {
		if !v.Local {
			delete(s.byID, k)
		}
	}
	s.mu.Unlock()
	s.save()
}

func (s *sessions) save() {
	s.mu.Lock()
	remote := map[string]*session{}
	for k, v := range s.byID {
		if !v.Local {
			remote[k] = v
		}
	}
	b, err := json.Marshal(remote)
	s.mu.Unlock()
	if err == nil {
		config.WriteFileAtomic(s.file, b, 0o600)
	}
}

// limiter slows down password guessing: a few failed logins per address,
// then a pause.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	maxFailures   = 8
	failureWindow = 15 * time.Minute
)

func (l *limiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if time.Since(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	l.failures[ip] = recent
	return len(recent) < maxFailures
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.failures[ip], time.Now())
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

func tokenEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sameOrigin rejects cross-site requests: a page on another site can't
// add our custom header without permission, and if the browser sends an
// Origin it must be this panel.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-VES") != "1" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return strings.EqualFold(o, r.Host)
}
