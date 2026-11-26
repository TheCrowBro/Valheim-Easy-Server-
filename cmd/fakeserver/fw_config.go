package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// configName is the file that configures one copy of the stand-in.
const configName = "fakeserver.json"

// Config is one copy's fakeserver.json. Every field may be left out.
type Config struct {
	// Game names the personality; empty means "recognise it from the
	// command line".
	Game string `json:"game,omitempty"`
	// Script is what players do, in seconds after the server is ready:
	// "join:Alex@1,death:Alex@5,deny:Bob@6,leave:Alex@9" (fw_script.go).
	Script string `json:"script,omitempty"`
	// Speed runs the game's own clocks (autosaves, loading) this many
	// times faster; 0 means 1. Script times are real seconds, so a test
	// can say exactly when a player joins.
	Speed float64 `json:"speed,omitempty"`
	// Version is what the server says it is; empty means the
	// personality's own.
	Version string `json:"version,omitempty"`
	// MemMB is the memory it holds while it runs; 0 means the
	// personality's own.
	MemMB int `json:"memMB,omitempty"`
	// CrashAfter makes it crash this many seconds after it started (0:
	// never), for the app's crash handling.
	CrashAfter float64 `json:"crashAfter,omitempty"`
	// NoReady keeps it from ever saying it's ready, as if an update had
	// changed that line, for the app's fallbacks. The script still runs.
	NoReady bool `json:"noReady,omitempty"`
	// Host is the address its ports listen on; empty means 127.0.0.1.
	// Nobody outside joins a stand-in, so by default it listens only on
	// this computer, which also never raises a firewall question.
	Host string `json:"host,omitempty"`
	// Options are a personality's own switches, such as a log line
	// worded the way an older version wrote it.
	Options map[string]string `json:"options,omitempty"`

	steps []Step // Script, read
}

// loadConfig reads fakeserver.json from dir or one of the three folders
// above it (a server's program can sit deep in its install, such as a
// private Java under runtime/<jdk>/bin). No file means every default, as
// when the demo's "real" install copies the program alone.
func loadConfig(dir string) (Config, error) {
	for range 4 {
		path := filepath.Join(dir, configName)
		b, err := os.ReadFile(path)
		if err == nil {
			return parseConfig(path, b)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("can't read %s: %v", path, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return Config{Speed: 1}, nil
}

// parseConfig reads one fakeserver.json strictly: a misspelt key is an
// error rather than a setting that silently does nothing.
func parseConfig(path string, b []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	if c.Speed < 0 || c.MemMB < 0 || c.CrashAfter < 0 {
		return Config{}, fmt.Errorf("%s: speed, memMB and crashAfter can't be negative", path)
	}
	if c.Speed == 0 {
		c.Speed = 1
	}
	steps, err := parseScript(c.Script)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %v", path, err)
	}
	c.steps = steps
	return c, nil
}
