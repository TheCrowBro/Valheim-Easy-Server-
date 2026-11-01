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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/selfupdate"
	"valheim-easy-server/internal/steamcmd"
	"valheim-easy-server/internal/tray"
	"valheim-easy-server/internal/web"
)

// version and build are set at build time with
// -ldflags "-X main.version=… -X main.build=…". build is the number of the
// GitHub build; copies with a number update themselves to newer builds.
var (
	version = "dev"
	build   = "0"
)

const appName = "Valheim Easy Server"

func main() {
	dataFlag := flag.String("data", "", "folder for settings, server files, worlds and logs (default: next to this program)")
	background := flag.Bool("background", false, "start without opening the control panel window (used when starting with Windows)")
	noWindow := flag.Bool("no-window", false, "don't open the control panel window")
	panelPort := flag.Int("port", web.DefaultPort, "port for the control panel (it only listens on this computer)")
	devServer := flag.String("dev-fake-server", "", "development only: install this program instead of downloading the real server")
	devDiscord := flag.String("dev-fake-discord", "", "development only: send Discord messages to this address instead of Discord")
	devGitHub := flag.String("dev-fake-github", "", "development only: look for app updates at this address instead of GitHub")
	detached := flag.Bool("detached", false, "used by the app itself on macOS")
	// Older macOS versions add a -psn_… argument when an app is opened.
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-psn_") {
			args = append(args[:i], args[i+1:]...)
			i--
		}
	}
	flag.CommandLine.Parse(args)
	for _, dev := range []string{*devDiscord, *devGitHub} {
		if dev != "" && !loopbackURL(dev) {
			platform.ShowError(appName, "The --dev-fake-* options only take an address on this computer, such as http://127.0.0.1:8460.")
			os.Exit(2)
		}
	}

	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	inMacApp := runtime.GOOS == "darwin" && strings.Contains(exe, ".app/Contents/MacOS/")
	if inMacApp && !*detached {
		// Opened from Finder (or at login): carry on in the background.
		// macOS only opens an app that isn't running, so opening it again
		// then starts a new copy, which shows the control panel.
		if err := platform.Detach(exe, append(args, "--detached")); err == nil {
			return
		}
	}
	selfupdate.CleanUp(exe) // a download an update didn't finish
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
			msg := "Valheim Easy Server is already running. Look for the shield icon next to the clock."
			if runtime.GOOS == "darwin" {
				msg = "Valheim Easy Server is already running."
			}
			platform.ShowError(appName, msg)
		}
		closeLog()
		return
	}

	opt := manager.Options{DataDir: dataDir, AppExe: exe, Logger: logger, DiscordURL: *devDiscord, AppVersion: version}
	if *devDiscord != "" {
		opt.DiscordAPI = strings.TrimRight(*devDiscord, "/") + "/api/v10" // where the waker's bot token is checked
	}
	var closeApp func(stopServer, restart bool)
	var restart atomic.Bool // start the updated program once this one has closed
	translocated := strings.Contains(exe, "/AppTranslocation/")
	if n, _ := strconv.Atoi(build); n > 0 && (runtime.GOOS == "windows" || runtime.GOOS == "darwin" && !translocated) {
		opt.AppBuild, opt.Updater = n, &selfupdate.Updater{API: *devGitHub}
		opt.Relaunch = func() { closeApp(false, true) }
	}
	if translocated {
		// macOS runs a downloaded app from a temporary copy until it's
		// moved somewhere, and that copy can't update itself.
		logger.Printf("running from a temporary copy (%s): updates are off", exe)
		go platform.ShowError(appName, "Please move Valheim Easy Server to your Applications folder (drag it there in Finder), and open it from there. Where it is now, macOS runs a temporary copy of it, which can't update itself.")
	}
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
	closeApp = func(stopServer, restartApp bool) {
		quitOnce.Do(func() {
			restart.Store(restartApp)
			go func() {
				if stopServer {
					stopAndWait(m, logger)
				}
				close(done)
			}()
		})
	}
	quit := func(stopServer bool) { closeApp(stopServer, false) }

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
		port := srv.Port()
		srv.Close()
		m.Close()
		os.Remove(filepath.Join(dataDir, ".instance.json"))
		release()
		if restart.Load() {
			startUpdated(exe, relaunchArgs(os.Args[1:], port), dataDir, logger)
		}
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

// loopbackURL reports whether s is a web address on this computer: the
// development stand-ins for Discord and GitHub can't be anywhere else.
func loopbackURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// relaunchArgs are the updated app's arguments: the same, but on the same
// port and without opening another window (an open control panel
// reconnects by itself).
func relaunchArgs(args []string, port int) []string {
	var out []string
	window := true
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if strings.HasPrefix(args[i], "-") {
			switch {
			case name == "port":
				if !hasValue {
					i++ // the port is the next argument
				}
				continue
			case (name == "background" || name == "no-window") && value != "false":
				window = false
			}
		}
		out = append(out, args[i])
	}
	if window {
		out = append(out, "--background")
	}
	return append(out, fmt.Sprintf("--port=%d", port))
}

// startUpdated starts the updated program and waits for its control panel.
// If it doesn't start, the previous version is put back and started
// instead, which tells the panel what happened.
func startUpdated(exe string, args []string, dataDir string, logger *log.Logger) {
	logger.Printf("starting the updated app")
	err := waitStarted(exec.Command(exe, args...), dataDir, time.Minute)
	if err == nil {
		return
	}
	logger.Printf("the updated app didn't start (%v): going back to the previous version", err)
	if err := selfupdate.Restore(exe); err != nil {
		logger.Printf("putting the previous version back: %v", err)
		return
	}
	if err := exec.Command(exe, args...).Start(); err != nil {
		logger.Printf("starting the previous version: %v", err)
	}
}

// waitStarted starts cmd, a copy of this app, and waits until its control
// panel answers. If it closes first or the panel doesn't answer in time,
// it's stopped.
func waitStarted(cmd *exec.Cmd, dataDir string, limit time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(limit)
	for !panelAnswers(dataDir) {
		select {
		case err := <-exited:
			if panelAnswers(dataDir) {
				return nil // another copy was started meanwhile
			}
			if err == nil {
				err = errors.New("it closed")
			}
			return err
		case <-deadline:
			cmd.Process.Kill()
			<-exited
			return errors.New("its control panel didn't open")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}

// panelAnswers reports whether the control panel noted in the data folder
// answers.
func panelAnswers(dataDir string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, ".instance.json"))
	var info instanceInfo
	if err != nil || json.Unmarshal(b, &info) != nil || info.Port == 0 {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", info.Port))
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
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
	if runtime.GOOS == "darwin" {
		// A Mac app keeps its files in ~/Library/Application Support, not
		// in the app, which gets replaced when it updates.
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(base, "ValheimEasyServer")
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
