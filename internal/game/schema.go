package game

import (
	"slices"
	"strings"
)

// ---- settings schema ----

// Scope says where a setting lives (spec 2.9).
type Scope string

const (
	ServerScope   Scope = "server"   // the instance
	WorldScope    Scope = "world"    // each world
	NewWorldScope Scope = "newWorld" // only when making a world
)

// Field is one setting the panel's generic form draws.
type Field struct {
	Key      string   `json:"key"`
	Scope    Scope    `json:"scope"`
	Kind     string   `json:"kind"` // "text", "password", "number", "bool", "choice", "list"
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Default  string   `json:"default,omitempty"`
	Min      int      `json:"min,omitempty"`
	Max      int      `json:"max,omitempty"`
	Choices  []Choice `json:"choices,omitempty"`
	Restart  bool     `json:"restart,omitempty"`  // applies after a restart
	Advanced bool     `json:"advanced,omitempty"` // under "More settings"
	Add      bool     `json:"add,omitempty"`      // asked in Add a game
}

// Choice is one option of a "choice" field. The JSON names are lower case
// like every other field the panel reads.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Schema is a game's settings, in the order the form shows them.
type Schema []Field

// FieldErrors maps a field key to a message. It has Error() like config.FieldErrors,
// and web.fail answers both the same way: {error, fields}.
type FieldErrors map[string]string

// Error joins the messages in the order of their keys, so the same
// problems always read the same way (as config.FieldErrors does).
func (e FieldErrors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, e[k])
	}
	return strings.Join(parts, " ")
}
