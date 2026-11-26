//go:build !race

package manager

// raceEnabled: the race detector slows everything down, so the tests skip
// their loose time bounds under it.
const raceEnabled = false
