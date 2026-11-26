package jetkvm

// Where the waker lives on the JetKVM (docs/dev/multigame/spec.md 1.2).
// The JetKVM keeps what any copy of the app put there, and every copy
// finds, updates, stops and removes the waker by these names, so they never
// change, even when the name people see does. Each value is compared with
// its literal: renaming one fails here.

import "testing"

func TestIdentity(t *testing.T) {
	for _, c := range []struct{ what, got, want string }{
		{"the waker this app carries", wakerFile, "dist/valheim-waker-armv7.gz"},
		{"the waker program", wakerName, "valheim-waker"},
		{"its start script", initScriptName, "S99valheim-waker"},
		{"the JetKVM's kept folder", defaultBase, "/userdata"},
		{"the JetKVM's folder in memory", defaultRun, "/tmp"},
		{"the start script's place", Target{}.script(), "/userdata/init.d/S99valheim-waker"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}
	base, run := Target{}.paths()
	if base != "/userdata" || run != "/tmp" {
		t.Errorf("folders = %q and %q, want /userdata and /tmp", base, run)
	}
	// The start script, as every copy since the waker came writes it: the
	// folder, program and files it names are where the next copy looks.
	if got := script(base, run); got != startScript {
		t.Errorf("the start script changed:\n%s\nwant:\n%s", got, startScript)
	}
}

const startScript = `#!/bin/sh
# Valheim Easy Server's Discord waker: wakes the server computer when
# someone types /valheim in Discord. The app put it here.
dir='/userdata/valheim-waker'
pid='/tmp/valheim-waker.pid'
log='/tmp/valheim-waker.log'
status='/tmp/valheim-waker.json'
running() {
	p=$(cat "$pid" 2>/dev/null) && [ -n "$p" ] && grep -q valheim-waker "/proc/$p/cmdline" 2>/dev/null
}
case "$1" in
start)
	running && exit 0
	if [ -f "$log" ] && [ "$(wc -c <"$log")" -gt 262144 ]; then mv -f "$log" "$log.old"; fi
	(trap '' HUP; exec "$dir/valheim-waker" -config "$dir/config.json" -status "$status") </dev/null >>"$log" 2>&1 &
	echo $! >"$pid"
	;;
stop)
	if running; then
		kill "$p"
		i=0
		while running && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
	fi
	rm -f "$pid" "$status"
	;;
status)
	if running; then cat "$status" 2>/dev/null; else echo stopped; tail -n 5 "$log" 2>/dev/null; fi
	# What it says is the answer. There's no status yet while the waker
	# starts, and no log before it first starts, which isn't a failure.
	exit 0
	;;
esac
`
