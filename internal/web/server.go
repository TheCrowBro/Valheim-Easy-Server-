// Package web serves the control panel: a small website inside the app
// that only this computer can use. It listens on 127.0.0.1 and turns away
// requests that other websites could make (see guard).
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
	"strconv"
	"sync"
	"time"

	"valheim-easy-server/internal/manager"
)

//go:embed ui
var uiFiles embed.FS

// DefaultPort is the control panel's usual port.
const DefaultPort = 8456

// Server is the control panel web server.
type Server struct {
	m       *manager.Manager
	log     *log.Logger
	version string
	quit    func(stopServer bool)
	handler http.Handler

	mu   sync.Mutex
	srv  *http.Server
	port int
}

// Options configure the server.
type Options struct {
	Manager *manager.Manager
	Logger  *log.Logger
	Version string
	Quit    func(stopServer bool) // closes the app
}

// New creates the server; call Listen to start it.
func New(o Options) *Server {
	s := &Server{m: o.Manager, log: o.Logger, version: o.Version, quit: o.Quit}
	s.handler = s.routes()
	return s
}

// Listen starts the server on this computer only. If port is taken, any
// free port is used instead.
func (s *Server) Listen(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return fmt.Errorf("couldn't open a port for the control panel: %w", err)
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          s.log,
	}
	s.mu.Lock()
	s.srv, s.port = srv, ln.Addr().(*net.TCPAddr).Port
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Printf("web server: %v", err)
		}
	}()
	s.log.Printf("control panel listening on %s", ln.Addr())
	return nil
}

// Port is the port the panel is on.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// URL is the control panel's address.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", s.Port())
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

// index serves the single-page app.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
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
