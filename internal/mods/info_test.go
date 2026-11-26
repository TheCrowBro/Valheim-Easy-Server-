package mods

import (
	"encoding/json"
	"testing"
)

// TestInfoJSON pins the JSON of Info (manager.ModInfo before it moved
// here): the mod's own fields at the top level, then the installed
// version, left out when there is none. The panel and the Discord texts
// read exactly this.
func TestInfoJSON(t *testing.T) {
	md := Mod{ID: "riverheim", Package: "Ns-Riverheim", Name: "Riverheim", Author: "Ns",
		Short: "rivers", Summary: "More rivers.", URL: "https://example.invalid/riverheim",
		Group: GroupWorld, Players: "required", NewWorlds: true}
	for _, c := range []struct {
		in   Info
		want string
	}{
		{Info{Mod: md, Version: "1.2.3"},
			`{"id":"riverheim","package":"Ns-Riverheim","name":"Riverheim","author":"Ns","short":"rivers","summary":"More rivers.","url":"https://example.invalid/riverheim","group":"world","players":"required","newWorlds":true,"version":"1.2.3"}`},
		{Info{Mod: md},
			`{"id":"riverheim","package":"Ns-Riverheim","name":"Riverheim","author":"Ns","short":"rivers","summary":"More rivers.","url":"https://example.invalid/riverheim","group":"world","players":"required","newWorlds":true}`},
	} {
		b, err := json.Marshal(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("got  %s\nwant %s", b, c.want)
		}
	}
}
