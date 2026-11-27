package gametest

import "time"

// How long gametest waits for things, with the stand-in and with a real
// server, in one place.
//
// The stand-in does everything at once and runs its clocks 60 times
// faster, so short waits keep a broken adapter's test quick. A real
// server is slower at all of it: it saves a new world in seconds or
// longer, its query port may answer a little after its log says it's
// online, and its downloads are hundreds of megabytes. The first
// real-server run on GitHub failed checks that only timed out on the
// stand-in's waits, so a real server gets waits sized for it. A real
// check that times out still fails, only later; what a real server needs
// beyond what the app itself allows is noted in the test's log instead.
const (
	// appSaveWait is how long the app waits for a console game's "saved"
	// line after typing its save line, before it exits (spec 2.11).
	appSaveWait = 10 * time.Second

	// probeLimit is the most one Probe call may take (spec 2.2), so it is
	// all an adapter can count on the app giving a call. gametest gives
	// every call this long, with the stand-in and with a real server
	// alike, so a probe that is too slow for the app fails here too.
	probeLimit = 3 * time.Second
)

// saveWait is how long Conformance waits for the "saved" event after
// typing a console game's save line. With the stand-in it is the app's
// own wait. A real server's save keeps going after the app has exited,
// so taking longer than the app waits isn't a failure there: the check is
// that the line saves at all, and Conformance notes when it took longer.
func saveWait(real bool) time.Duration {
	if real {
		return 2 * time.Minute
	}
	return appSaveWait
}

// readyAfterTakeover is how long Conformance waits, after a takeover, for
// the new Monitor to have replayed the log up to the server being ready.
// A real server's log can be long (Terraria writes a line per percent of
// every step of making a world).
func readyAfterTakeover(real bool) time.Duration {
	if real {
		return time.Minute
	}
	return 15 * time.Second
}

// sendTimeout bounds sending one stop step (an RCON command or an API
// call). The manager gives a call the stop job's whole time; a real server
// asked to save over its API may take a while to answer.
func sendTimeout(real bool) time.Duration {
	if real {
		return 2 * time.Minute
	}
	return 10 * time.Second
}

// probeTries is how long Conformance keeps trying the probe after the
// server is online, and how long each try may take. The stand-in answers
// at once, so one try is all it gets; a real server's query port may
// start answering a little after its log says it's online, so it gets a
// minute of tries. Each try still gets only probeLimit, as in the app,
// which also probes over and over (spec 2.6), each call held to the
// contract's 3 s: a real check is the only place a real probe's speed is
// tested, and a probe that needed longer would pass here yet time out on
// every call in the app.
func probeTries(real bool) (total, each time.Duration) {
	if real {
		return time.Minute, probeLimit
	}
	return 0, probeLimit
}

// portGrace is how long, with a real server, checkPorts waits for the
// system to show a public port held, before checking it: a server may open
// a port (a query port, say) a moment after its log says it's online. The
// stand-in opens every port before it says so.
func portGrace(real bool) time.Duration {
	if real {
		return 30 * time.Second
	}
	return 0
}

// httpTimeout bounds each HTTP request the adapter makes through Env.HTTP,
// a download included (http.Client's Timeout covers reading the whole
// answer). The stand-in downloads nothing; a real server's download can
// be a few hundred megabytes.
func httpTimeout(real bool) time.Duration {
	if real {
		return 30 * time.Minute
	}
	return 5 * time.Minute
}
