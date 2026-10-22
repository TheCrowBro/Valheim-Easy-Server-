// Package web serves the control panel: a small website that runs inside
// the app. The app's own window logs in with a secret token; friends on
// other devices log in with the control panel password.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/manager"
)

//go:embed ui
var uiFiles embed.FS

// Server is the control panel web server.
type Server struct {
	m        *manager.Manager
	log      *log.Logger
	token    string // lets the app's own window in without a password
	sessions *sessions
	limit    *limiter
	version  string
	quit     func(stopServer bool)
	handler  http.Handler

	mu     sync.Mutex
	srv    *http.Server
	ln     net.Listener
	port   int
	remote bool
}

// Options configure the server.
type Options struct {
	Manager *manager.Manager
	Logger  *log.Logger
	DataDir string
	Version string
	Quit    func(stopServer bool) // closes the app
}

// New creates the server; call Listen to start it.
func New(o Options) *Server {
	s := &Server{
		m:        o.Manager,
		log:      o.Logger,
		token:    config.RandomToken(24),
		sessions: newSessions(filepath.Join(o.DataDir, "sessions.json")),
		limit:    &limiter{failures: map[string][]time.Time{}},
		version:  o.Version,
		quit:     o.Quit,
	}
	s.handler = s.routes()
	return s
}

// Token is the secret the app window uses to log in.
func (s *Server) Token() string { return s.token }

// Listen starts (or moves) the server according to the panel settings.
// Without remote access it only listens on this computer. If the port is
// taken and remote access is off, any free port is used instead.
func (s *Server) Listen(p config.Panel) error {
	s.mu.Lock()
	if s.ln != nil && s.port == p.Port && s.remote == p.Remote {
		s.mu.Unlock()
		return nil // nothing changes
	}
	oldSrv, oldPort, oldRemote := s.srv, s.port, s.remote
	s.mu.Unlock()
	if oldSrv != nil && oldPort == p.Port {
		// Same port on different network interfaces: free it first.
		oldSrv.Close()
		oldSrv = nil
	}
	ln, err := listen(p.Port, p.Remote)
	if err != nil && !p.Remote {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		if oldSrv == nil && oldPort != 0 {
			// Put things back the way they were.
			if back, berr := listen(oldPort, oldRemote); berr == nil {
				s.serve(back, oldRemote, nil)
			}
		}
		return fmt.Errorf("couldn't open port %d for the control panel (is another program using it?): %w", p.Port, err)
	}
	s.serve(ln, p.Remote, oldSrv)
	return nil
}

func listen(port int, remote bool) (net.Listener, error) {
	host := "127.0.0.1"
	if remote {
		host = "" // every network interface
	}
	return net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// serve starts serving on ln and shuts down old (if any) gracefully.
func (s *Server) serve(ln net.Listener, remote bool, old *http.Server) {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          s.log,
	}
	s.mu.Lock()
	s.srv, s.ln = srv, ln
	s.port = ln.Addr().(*net.TCPAddr).Port
	s.remote = remote
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Printf("web server: %v", err)
		}
	}()
	if old != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			old.Shutdown(ctx)
		}()
	}
	s.log.Printf("control panel listening on %s (remote access %v)", ln.Addr(), remote)
}

// Port is the port the panel is on.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// LocalURL opens the panel on this computer, already logged in.
func (s *Server) LocalURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.Port(), s.token)
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}
}

// ServeHTTP lets tests use the server without a network listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

type ctxKey int

const sessionKey ctxKey = 1

func sessionOf(r *http.Request) session {
	v, _ := r.Context().Value(sessionKey).(session)
	return v
}

// auth wraps API handlers: it requires a login, blocks cross-site
// requests, and optionally limits the call to the server computer.
func (s *Server) auth(localOnly bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(cookieName)
		var sess session
		ok := false
		if c != nil {
			sess, ok = s.sessions.get(c.Value)
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "Please log in.")
			return
		}
		if sess.Local && !isLoopback(r) {
			writeErr(w, http.StatusUnauthorized, "Please log in.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "This request was blocked for safety. Reload the page and try again.")
			return
		}
		if localOnly && !sess.Local {
			writeErr(w, http.StatusForbidden, "This can only be done on the server computer itself.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// index serves the single-page app. Opening it with ?token=… (the app's
// own window) logs in and removes the token from the address.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		if tokenEqual(tok, s.token) && isLoopback(r) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.sessions.create(true), Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	b, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *Server) static() http.Handler {
	sub, _ := fs.Sub(uiFiles, "ui")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
