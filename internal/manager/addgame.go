package manager

// Add a game (spec 6.1 and 6.2, stream A): the catalog of every game this
// build can run, the owner's own worlds for a game not yet added, and
// adding one: its servers.json entry with ports picked (ports.go), the
// licence accepted (game.StateEULA), the star only when asked (10.2 Q3),
// its first world, and the install job.
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in.

import (
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/worlds"
)

// CatalogGame is one game in Add a game (GET /api/catalog).
type CatalogGame struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Short       string            `json:"short"`
	Letter      string            `json:"letter"`
	Color       string            `json:"color"`
	Stage       game.Stage        `json:"stage"`     // "preview" is labelled; hidden games aren't listed
	CanRun      game.Level        `json:"canRun"`    // on this computer, from Info.OS
	NeedsText   string            `json:"needsText"` // "Needs Windows", when it can't
	DownloadMB  int               `json:"downloadMB"`
	DiskMB      int               `json:"diskMB"`
	MemoryMB    int               `json:"memoryMB"`
	MaxPlayers  int               `json:"maxPlayers"`
	FriendsNeed string            `json:"friendsNeed"`
	Password    game.PasswordRule `json:"password"`
	Schema      game.Schema       `json:"schema"` // the fields asked in Add a game (Field.Add)
	Ports       []game.PortRole   `json:"ports"`
	Import      bool              `json:"import"`         // worlds can come from the owner's own game
	EULA        string            `json:"eula,omitempty"` // the licence to accept first (game.Licensed)
	Added       bool              `json:"added"`          // there is an instance of it already
}

// Catalog lists every game that isn't hidden, in Info.Order.
func (m *Manager) Catalog() []CatalogGame {
	return []CatalogGame{}
}

// CatalogLocal lists the worlds in the owner's own copy of a game on this
// computer, for a game not added yet (GET /api/catalog/{game}/local).
func (m *Manager) CatalogLocal(gameID string) ([]worlds.Candidate, error) {
	return nil, errNotYet
}

// AddGameRequest is POST /api/games.
type AddGameRequest struct {
	Game       string      `json:"game"`
	ServerName string      `json:"serverName"`
	Password   string      `json:"password"`
	Values     game.Values `json:"values,omitempty"` // the other Add fields of the server scope, by key
	EULA       bool        `json:"eula"`             // the owner ticked the licence box
	Star       bool        `json:"star"`             // "Star it" (unticked by default)
	World      AddWorld    `json:"world"`
}

// AddWorld is the first world of a game being added.
type AddWorld struct {
	Mode      string      `json:"mode"` // "new" or "import"
	Name      string      `json:"name"`
	Values    game.Values `json:"values,omitempty"`    // new-world values, by key
	Candidate string      `json:"candidate,omitempty"` // "import": a CatalogLocal candidate's ID
}

// AddedGame is what adding a game answers: its instance and install job.
type AddedGame struct {
	ID  string `json:"id"`
	Job int    `json:"job"`
}

// AddGame adds a game and starts installing it. Adding a hidden legacy game
// only shows it again (I1). Field errors are keyed "server.<key>",
// "newWorld.<key>".
func (m *Manager) AddGame(in AddGameRequest) (AddedGame, error) {
	return AddedGame{}, errNotYet
}
