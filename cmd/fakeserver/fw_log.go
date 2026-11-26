package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// The log is where the server writes its lines: standard output until the
// personality calls LogTo, as a real server does when the app captures its
// output, or a file the command line names (Valheim's -logFile; a game
// that writes its own log). Lines are written one by one, unbuffered, so
// the app sees each as soon as it happens and nothing is lost on exit.

// LogTo sends the log to a file (appending, as servers do), or back to
// standard output when path is "". A file that can't be opened stops the
// server, as it would stop a real one.
func (s *Server) LogTo(path string) {
	out := os.Stdout
	if path != "" {
		os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fail("can't open the log file: %v", err)
		}
		out = f
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.out != os.Stdout {
		s.out.Close()
	}
	s.out = out
}

// Log writes one line as it is: the personality adds its game's own
// timestamp and prefix.
func (s *Server) Log(line string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.out.WriteString(line + "\n")
}

// Logf writes one formatted line.
func (s *Server) Logf(format string, args ...any) {
	s.Log(fmt.Sprintf(format, args...))
}

// Warnf reports a problem with the stand-in itself (not the game) on
// standard error, which the app also captures.
func (s *Server) Warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakeserver: "+format+"\n", args...)
}

// syncLog flushes the log file to disk before the program exits.
func (s *Server) syncLog() {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.out != os.Stdout {
		s.out.Sync()
	}
}
