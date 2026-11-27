// Command devfixtures serves the control panel with answers recorded from
// the app, so the new panel (docs/dev/multigame/spec.md 6.1) can be built
// and looked at before the manager runs every game (6.2: "the UI stream
// works against recorded status and settings JSON fixtures served by a
// small dev handler").
//
//	go run ./internal/web/devfixtures            # http://127.0.0.1:8457/
//
// Every answer in recorded/ is one the app itself gave: internal/web's
// TestRecordDevFixtures runs the app's own manager and routes with the
// stand-in servers, puts a few games in the states the panel shows, and
// writes down what each route answered. To record them again (after the
// app's answers change, which internal/web's tests point out):
//
//	go test ./internal/web -run TestRecordDevFixtures -record-devfixtures
//
// This program makes up no answer of its own, so it can't drift from the
// app the way a second copy of the manager would. It keeps two things in
// memory, because they are the panel's own choices and the sidebar can't
// be tried without them: the stars, and which sidebar lists are open. Any
// other change is answered as the app answered that very request when it
// was recorded (the start refused for want of memory), or else with the
// app's {"ok": true}, and changes nothing. To watch a game install, start
// and stop, run the app itself with --dev-fake-server and
// EGS_DEV_FAKE_SERVERS (spec 7.1).
//
// It answers exactly the app's routes (web.Patterns) behind the app's own
// guard (web.Protect): a route the app doesn't have is a 404, and a change
// without the panel's X-VES header is refused, as in the app. A message
// it writes itself starts with "devfixtures:", so it is never mistaken for
// one of the app's. The panel's files are read from internal/web/ui on
// every request, so a change shows on reload.
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
)

func main() {
	port := flag.Int("port", 8457, "the port to listen on (this computer only)")
	ui := flag.String("ui", besideSource("ui"), "the panel's folder (internal/web/ui)")
	recorded := flag.String("recorded", besideSource("devfixtures/recorded"), "the recorded answers")
	flag.Parse()

	if _, err := os.Stat(filepath.Join(*ui, "index.html")); err != nil {
		log.Fatalf("no panel at %s: run this from the repository, or give -ui", *ui)
	}
	f, err := load(*recorded, *ui, log.Default())
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("the control panel with recorded answers: http://%s/", ln.Addr())
	srv := &http.Server{Handler: f.handler(), ReadHeaderTimeout: 15 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// besideSource is a folder of internal/web, found from where this file's
// source is, so `go run` works from anywhere in the repository; else the
// same path from the working folder.
func besideSource(rel string) string {
	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Join(filepath.Dir(file), "..", filepath.FromSlash(rel))
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return filepath.Join("internal", "web", filepath.FromSlash(rel))
}
