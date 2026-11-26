package vintagestory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyConfig(t *testing.T) {
	v := configValues{ServerName: "A <b> & c", Port: 42500, MaxClients: 4, Password: "pw", World: "Wörld",
		SaveFile: `C:\Users\Owner\saves\Saves\Wörld.vcdbs`, New: true, Style: styleOf("creativebuilding")}

	// The server writes a BOM-less file; a BOM from a hand edit is fine
	// too, and a WorldConfig of null or none gets one.
	for _, in := range []string{
		`{"Roles":[{"Code":"suplayer"}],"WorldConfig":null}`,
		"\ufeff" + `{"Roles":[{"Code":"suplayer"}]}`,
	} {
		out, err := applyConfig([]byte(in), v)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		var c struct {
			ServerName, Password string
			Port, MaxClients     int
			WhitelistMode        int
			AdvertiseServer      *bool
			Upnp                 *bool
			OnlyWhitelisted      *bool
			WorldConfig          map[string]any
		}
		if err := json.Unmarshal(out, &c); err != nil {
			t.Fatalf("%q gave %s: %v", in, out, err)
		}
		if c.ServerName != v.ServerName || c.Password != "pw" || c.Port != 42500 || c.MaxClients != 4 || c.WhitelistMode != 1 ||
			c.AdvertiseServer == nil || *c.AdvertiseServer || c.Upnp == nil || *c.Upnp || c.OnlyWhitelisted != nil {
			t.Errorf("%q gave %s", in, out)
		}
		wc := c.WorldConfig
		if wc["SaveFileLocation"] != v.SaveFile || wc["WorldName"] != "Wörld" || wc["Seed"] != nil || wc["PlayStyle"] != "creativebuilding" || wc["WorldType"] != "superflat" {
			t.Errorf("WorldConfig = %v", wc)
		}
		if strings.Contains(string(out), `\u003c`) {
			t.Errorf("the name was escaped for HTML: %s", out)
		}
	}

	// An existing world: its style and seed are left as the file has them.
	out, err := applyConfig([]byte(`{"Roles":[1],"WorldConfig":{"Seed":"77","PlayStyle":"exploration","PlayStyleLangCode":"preset-exploration","WorldType":"standard","Extra":[1,2]}}`),
		configValues{World: "Old", SaveFile: "/s/Old.vcdbs"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"WorldConfig": {
    "Seed": "77",
    "PlayStyle": "exploration",
    "PlayStyleLangCode": "preset-exploration",
    "WorldType": "standard",
    "Extra": [
      1,
      2
    ],
    "WorldName": "Old",
    "SaveFileLocation": "/s/Old.vcdbs"
  }`; !strings.Contains(string(out), want) {
		t.Errorf("existing world:\n%s", out)
	}

	for _, bad := range []string{``, `[]`, `{"a":1`, `{"a":1} {"b":2}`, `{"WorldConfig":[1]}`} {
		if _, err := applyConfig([]byte(bad), v); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if err := usable([]byte(`{"Roles":[]}`)); err == nil {
		t.Error("a file without roles is usable")
	}
	if err := usable([]byte(`{"Roles":[{"Code":"suplayer"}]}`)); err != nil {
		t.Error(err)
	}
}
