package game

import (
	"reflect"
	"testing"
)

func TestValuesInt(t *testing.T) {
	v := Values{"port": "25565", "spaced": " 7 ", "neg": "-3", "bad": "lots", "empty": "", "float": "2.5"}
	for _, c := range []struct {
		key  string
		def  int
		want int
	}{
		{"port", 1, 25565},
		{"spaced", 1, 7},
		{"neg", 1, -3},
		{"bad", 10, 10},
		{"empty", 10, 10},
		{"float", 10, 10},
		{"missing", 42, 42},
	} {
		if got := v.Int(c.key, c.def); got != c.want {
			t.Errorf("Int(%q, %d) = %d, want %d", c.key, c.def, got, c.want)
		}
	}
	var none Values
	if got := none.Int("port", 9); got != 9 {
		t.Errorf("nil Values: Int = %d, want the default", got)
	}
}

func TestValuesBool(t *testing.T) {
	v := Values{"on": "true", "off": "false", "one": "1", "upper": "TRUE", "spaced": " true ", "yes": "yes", "empty": ""}
	for key, want := range map[string]bool{
		"on": true, "off": false, "one": true, "upper": true, "spaced": true,
		"yes": false, "empty": false, "missing": false,
	} {
		if got := v.Bool(key); got != want {
			t.Errorf("Bool(%q) = %v, want %v", key, got, want)
		}
	}
	var none Values
	if none.Bool("on") {
		t.Error("nil Values: Bool is on")
	}
}

func TestValuesClone(t *testing.T) {
	var none Values
	if none.Clone() != nil {
		t.Error("a nil Values cloned into a non-nil one")
	}
	v := Values{"a": "1"}
	c := v.Clone()
	c["a"] = "2"
	c["b"] = "3"
	if v["a"] != "1" || len(v) != 1 {
		t.Errorf("changing the clone changed the original: %v", v)
	}
}

func TestSavedClone(t *testing.T) {
	s := Saved{
		Server: Values{"serverName": "Friends"},
		Worlds: map[string]Values{"Riverbend": {"difficulty": "normal"}},
		World:  "Riverbend",
		Ports:  map[string]int{"game": 25565},
		State:  Values{"eula": "2026-12-03"},
	}
	c := s.Clone()
	if !reflect.DeepEqual(c, s) {
		t.Fatalf("clone differs: %+v", c)
	}
	c.Server["serverName"] = "x"
	c.Worlds["Riverbend"]["difficulty"] = "hard"
	c.Worlds["New"] = Values{}
	c.Ports["game"] = 1
	c.State["eula"] = ""
	if s.Server["serverName"] != "Friends" || s.Worlds["Riverbend"]["difficulty"] != "normal" ||
		len(s.Worlds) != 1 || s.Ports["game"] != 25565 || s.State["eula"] != "2026-12-03" {
		t.Errorf("changing the clone changed the original: %+v", s)
	}
	if empty := (Saved{}).Clone(); !reflect.DeepEqual(empty, Saved{}) {
		t.Errorf("an empty Saved cloned into %+v", empty)
	}
}
