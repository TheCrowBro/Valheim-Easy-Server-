// Command devfixtures serves the control panel with made-up games, so the
// new panel (docs/dev/multigame/spec.md 6.1) can be built and looked at
// without real servers, before the manager runs every game (6.2: "the UI
// stream works against recorded status and settings JSON fixtures served
// by a small dev handler").
//
//	go run ./internal/web/devfixtures                  # http://127.0.0.1:8457/
//	go run ./internal/web/devfixtures -os darwin -arch amd64   # as the Mac Pro: Windows-only games greyed
//	go run ./internal/web/devfixtures -free 3000       # little free memory: starts show the memory warning
//
// The panel's files are read from internal/web/ui on every request, so a
// change shows on reload. Every answer is made from the app's own types
// (manager.Status, manager.GameSettingsView, …) and each game's own
// adapter (its schema, ports, lists and how friends join), so the shapes
// are the real ones; only the numbers, names and timings are made up.
// Changes the panel asks for (stars, the sidebar, starting and stopping,
// settings, worlds, adding and removing a game) change these made-up games
// until it is stopped. It never starts a server, opens a port beyond its
// own or writes outside a temporary folder.
//
// For the app itself with stand-in servers, run the app with
// --dev-fake-server and EGS_DEV_FAKE_SERVERS (spec 7.1) instead.
//
// It is never shipped: nothing in the app imports it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "valheim-easy-server/internal/games/all" // every game the app registers
)

func main() {
	port := flag.Int("port", 8457, "the port to listen on (this computer only)")
	goos := flag.String("os", runtime.GOOS, "the system to pretend to be: windows, darwin or linux")
	goarch := flag.String("arch", runtime.GOARCH, "the processor to pretend to have: amd64 or arm64")
	free := flag.Int("free", 12000, "free memory in MB, for the memory warning")
	ui := flag.String("ui", defaultUI(), "the panel's folder (internal/web/ui)")
	speed := flag.Duration("step", 2*time.Second, "how long each pretend install, start and stop takes")
	flag.Parse()

	if _, err := os.Stat(filepath.Join(*ui, "index.html")); err != nil {
		log.Fatalf("no panel at %s: run this from the repository, or give -ui", *ui)
	}
	tmp, err := os.MkdirTemp("", "egs-devfixtures-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	st := newState(options{GOOS: *goos, GOARCH: *goarch, FreeMB: *free, Step: *speed, Dir: tmp})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("the control panel with made-up games: http://%s/ (as %s/%s, %d MB free)", ln.Addr(), *goos, *goarch, *free)
	srv := &http.Server{Handler: newHandler(st, *ui), ReadHeaderTimeout: 15 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// defaultUI is internal/web/ui next to this file's folder, found from where
// the source is, so `go run` works from anywhere in the repository; else
// the same path from the working folder.
func defaultUI() string {
	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Join(filepath.Dir(file), "..", "ui")
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return filepath.Join("internal", "web", "ui")
}
