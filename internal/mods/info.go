package mods

// Info is a mod the app offers, with the version installed, if any.
//
// It was manager.ModInfo. It lives here so the game contract
// (internal/game) can name it without importing the manager; the manager
// keeps ModInfo as an alias of it, so the panel's JSON is unchanged.
type Info struct {
	Mod
	Version string `json:"version,omitempty"`
}
