// Package all registers every game this build of the app can host. It is
// imported once, by main, so the manager and the web server never name a
// game themselves: they find each one through internal/game's registry.
//
// Each game has its own file here with one blank import, so adding a game
// is one new file, and a game's stream never edits another's line.
package all
