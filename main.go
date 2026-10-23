// Valheim Easy Server runs a Valheim dedicated server on this computer and
// gives it a friendly control panel: start and stop, worlds, backups,
// players, usage stats and hibernation. The panel opens in its own window
// on this computer.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/tray"
	"valheim-easy-server/internal/web"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const appName = "Valheim Easy Server"

func main() {
	dataFlag := flag.String("data", "", "folder for settings, server files, worlds and logs (default: next to this program)")
	background := flag.Bool("background", false, "start without opening the control panel window (used when starting with Windows)")
	noWindow := flag.Bool("no-window", false, "don't open the control panel window")
	panelPort := flag.Int("port", web.DefaultPort, "port for the control panel (it only listens on this computer)")
	devServer := flag.String("dev-fake-server", "", "development only: install this program instead of downloading the real server")
	devDiscord := flag.String("dev-fake-discord", "", "development only: send Discord messages to this address instead of Discord")
	flag.Parse()

	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	dataDir, err := resolveDataDir(*dataFlag, exe)
	if err != nil {
		platform.ShowError(appName, "Can't find a folder to keep the server's files in:\n"+err.Error())
		os.Exit(1)
	}
	logger, closeLog := openLog(dataDir)
	logger.Printf("%s %s starting (data folder %s)", appName, version, dataDir)

	// One copy per data folder: a second start just opens the window.
	id := sha256.Sum256([]byte(strings.ToLower(dataDir)))
	release, already, err := platform.SingleInstance(hex.EncodeToString(id[:8]))
	if err != nil {
		logger.Printf("single instance check: %v", err)
		release = func() {}
	}
	if already {
		if !openExisting(dataDir) {
			platform.ShowError(appName, "Valheim Easy Server is already running. Look for the shield icon next to the clock.")
		}
		closeLog()
		return
	}

	opt := manager.Options{DataDir: dataDir, AppExe: exe, Logger: logger, DiscordURL: *devDiscord}
	if *devServer != "" {
		src, _ := filepath.Abs(*devServer)
		opt.NewInstaller = func(p config.Paths) steamcmd.Installer {
			return &steamcmd.Fake{Source: src, InstallDir: p.Server, ExeName: config.ServerExeName(), Duration: 6 * time.Second}
		}
		opt.HibernateStatus = func() (bool, string) { return true, "" }
		opt.Hibernate = func() error { logger.Printf("development mode: pretending to hibernate"); return nil }
		opt.SleepStatus = func() (bool, string) { return true, "" }
		opt.Sleep = func() error { logger.Printf("development mode: pretending to sleep"); return nil }
	}
	m, err := manager.New(opt)
	if err != nil {
		platform.ShowError(appName, "Couldn't start:\n"+err.Error())
		os.Exit(1)
	}

	done := make(chan struct{})
	var quitOnce sync.Once
	quit := func(stopServer bool) {
		quitOnce.Do(func() {
			go func() {
				if stopServer {
					stopAndWait(m, logger)
				}
				close(done)
			}()
		})
	}

	srv := web.New(web.Options{Manager: m, Logger: logger, Version: version, Quit: quit})
	if err := srv.Listen(*panelPort); err != nil {
		platform.ShowError(appName, "Couldn't open the control panel:\n"+err.Error())
		os.Exit(1)
	}
	writeInstance(dataDir, srv)
	m.Start()

	url := srv.URL()
	fmt.Printf("%s is running. Control panel: %s\n", appName, url)
	if !*background && !*noWindow {
		if err := platform.OpenAppWindow(url); err != nil {
			logger.Printf("opening window: %v", err)
		}
	}

	// Ctrl+C events are how the server is asked to save and stop; this app
	// must never be stopped by one. Outside Windows, Ctrl+C closes the app.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		for s := range sig {
			if runtime.GOOS == "windows" {
				logger.Printf("ignored %v", s)
				continue
			}
			quit(false)
		}
	}()

	// Closing: stop serving, save data, and exit. This doesn't depend on
	// the tray, which may not exist (or may not stop) on some systems.
	go func() {
		<-done
		logger.Printf("closing")
		srv.Close()
		m.Close()
		os.Remove(filepath.Join(dataDir, ".instance.json"))
		release()
		closeLog()
		os.Exit(0)
	}()

	// The tray must run on the main thread (Windows delivers its window
	// messages to the thread that created it).
	tray.Run(tray.Options{
		Tooltip: func() string {
			st := m.Status()
			text := "Valheim server: " + map[manager.State]string{
				manager.StateOnline: "online", manager.StateStarting: "starting", manager.StateStopping: "stopping",
				manager.StateStopped: "offline", manager.StateNotInstalled: "not installed",
			}[st.State]
			if st.State == manager.StateOnline {
				text += fmt.Sprintf(", %d playing", len(st.Players))
			}
			return text
		},
		Running: func() bool {
			s := m.Status().State
			return s == manager.StateOnline || s == manager.StateStarting
		},
		Open:  func() { platform.OpenAppWindow(srv.URL()) },
		Start: func() { trayAction(m.StartServer()) },
		Stop: func() {
			if confirmPlayers(m, "stop the server") {
				trayAction(m.StopServer())
			}
		},
		RestLabel: func() string { label, _ := restWords(m); return label },
		Hibernate: func() {
			_, question := restWords(m)
			if platform.Confirm(appName, question+"\n\nWake it with Wake-on-LAN (for example from an IP KVM) or its power button.") {
				trayAction(m.Hibernate(""))
			}
		},
		Quit: func(stopServer bool) {
			if stopServer && !confirmPlayers(m, "stop the server") {
				return
			}
			quit(stopServer)
		},
		Changes: m.Subscribe,
	}, done)
	<-done
	select {} // the closing goroutine exits the program
}

// restWords gives the tray's menu label and question for resting the
// computer, which depend on whether it hibernates or sleeps.
func restWords(m *manager.Manager) (label, question string) {
	if m.Settings().RestMode == config.RestSleep {
		return "Put computer to sleep", "Save the world, stop the server and put this computer to sleep?"
	}
	return "Hibernate computer", "Save the world, stop the server and hibernate this computer?"
}

func trayAction(err error) {
	if err != nil {
		platform.ShowError(appName, err.Error())
	}
}

func confirmPlayers(m *manager.Manager, what string) bool {
	n := len(m.Status().Players)
	if n == 0 {
		return true
	}
	return platform.Confirm(appName, fmt.Sprintf("%d player(s) are online. Do you want to %s? The world is saved first.", n, what))
}

// stopAndWait saves and stops the server before the app closes.
func stopAndWait(m *manager.Manager, logger *log.Logger) {
	if err := m.StopServer(); err != nil {
		logger.Printf("stop before quitting: %v", err)
		return
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if s := m.Status().State; s == manager.StateStopped || s == manager.StateNotInstalled {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// resolveDataDir picks where the app keeps everything. By default that's
// the folder this program is in, so the whole setup can be moved or backed
// up as one folder. Run from Downloads or the Desktop, it uses a subfolder
// instead of scattering files there; if the folder can't be written (for
// example under Program Files), it uses the user's local app data.
func resolveDataDir(flagValue, exe string) (string, error) {
	if flagValue != "" {
		dir, err := filepath.Abs(flagValue)
		if err != nil {
			return "", err
		}
		return dir, os.MkdirAll(dir, 0o755)
	}
	dir := filepath.Dir(exe)
	switch strings.ToLower(filepath.Base(dir)) {
	case "downloads", "desktop":
		dir = filepath.Join(dir, appName)
	}
	if strings.HasPrefix(strings.ToLower(dir), strings.ToLower(os.TempDir())) && os.TempDir() != "" {
		dir = "" // running from a temp folder (e.g. straight out of a zip)
	}
	if dir != "" && writable(dir) {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			base = local
		}
	}
	dir = filepath.Join(base, "ValheimEasyServer")
	return dir, os.MkdirAll(dir, 0o755)
}

func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// openLog writes the app's own log to logs/app.log (and stderr), keeping
// the previous run's log as app.log.old when it gets big.
func openLog(dataDir string) (*log.Logger, func()) {
	dir := filepath.Join(dataDir, "logs")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "app.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return log.New(os.Stderr, "", log.LstdFlags), func() {}
	}
	var w io.Writer = f
	if runtime.GOOS != "windows" {
		w = io.MultiWriter(f, os.Stderr)
	}
	return log.New(w, "", log.LstdFlags), func() { f.Close() }
}

type instanceInfo struct {
	Port int `json:"port"`
}

// writeInstance lets a second start of the app open this copy's window.
func writeInstance(dataDir string, srv *web.Server) {
	b, _ := json.Marshal(instanceInfo{Port: srv.Port()})
	config.WriteFileAtomic(filepath.Join(dataDir, ".instance.json"), b, 0o644)
}

func openExisting(dataDir string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, ".instance.json"))
	if err != nil {
		return false
	}
	var info instanceInfo
	if json.Unmarshal(b, &info) != nil || info.Port == 0 {
		return false
	}
	err = platform.OpenAppWindow(fmt.Sprintf("http://127.0.0.1:%d/", info.Port))
	return !errors.Is(err, os.ErrNotExist) && err == nil
}
