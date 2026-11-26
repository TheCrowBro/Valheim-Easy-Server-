package game

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Record is the run file: runtime.json for the legacy instance, games/<id>/run.json for others.
// Its first fields are exactly Valheim's runInfo (manager.go:173-201).
//
// The run file is how a new copy of the app takes over a server that is
// already running (after an update or a rollback), so for the legacy
// instance it must stay key for key what older builds write and read
// (invariant I4, TestRuntimeJSONCompat).
type Record struct {
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Launched  time.Time `json:"launched"`
	LogPath   string    `json:"logPath"`
	World     string    `json:"world"`
	ArgsKey   string    `json:"argsKey"` // the fingerprint
	Exe       string    `json:"exe"`
	Announced string    `json:"announced,omitempty"`
	Posted    time.Time `json:"posted,omitempty"`
	PostError string    `json:"postError,omitempty"`
	Played    Played    `json:"played"` // no omitempty, exactly as bed004b writes it; owned by the manager for every instance
	Server    string    `json:"server,omitempty"`
	Port      int       `json:"port,omitempty"`
	Password  string    `json:"password,omitempty"`
	// Never written for the legacy instance:
	Game     string            `json:"game,omitempty"`
	Instance string            `json:"instance,omitempty"`
	Name     string            `json:"name,omitempty"` // the game's Short name, for older builds' "is running" warning
	Ports    map[string]int    `json:"ports,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"` // RCON/REST passwords the run started with
	// Extra holds the adapter's own fields, read and written at the top level of the
	// file (Valheim: "noCrossplay", "modsCode", "codeMods"), so runtime.json keeps its
	// shape. MarshalJSON/UnmarshalJSON do the inlining.
	Extra map[string]json.RawMessage `json:"-"`
}

// Played is what a run started with, for the reminders (today's manager.played, moved).
// Mods == nil means "started by an older version"; non-legacy instances write {} rather
// than null, so the lastplayed.json load rule (Mods != nil) holds for them too.
type Played struct {
	Game     string            `json:"game,omitempty"`
	Mods     map[string]string `json:"mods"`
	Password string            `json:"password,omitempty"` // a hash, enough to notice a change
}

// recordFields is Record without its JSON methods, so encoding/json handles
// the named fields the usual way and the methods below only add Extra.
type recordFields Record

// recordKeys are the JSON names of Record's own fields. A key in the file
// that matches one of them (as encoding/json matches: ignoring case) is a
// field; every other key belongs to Extra.
var recordKeys = func() []string {
	t := reflect.TypeFor[recordFields]()
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
			continue
		case name == "":
			name = f.Name
		}
		keys = append(keys, name)
	}
	return keys
}()

func isRecordKey(key string) bool {
	return slices.ContainsFunc(recordKeys, func(k string) bool { return strings.EqualFold(k, key) })
}

// MarshalJSON writes the named fields as usual, then each Extra key at the
// top level, sorted so the file doesn't change from one write to the next.
// An Extra key that names one of Record's own fields is refused: reading
// the file back would put its value in that field, so it is an adapter bug,
// and failing every write makes a test catch it at once.
func (r Record) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(recordFields(r))
	if err != nil {
		return nil, err
	}
	if len(r.Extra) == 0 {
		return base, nil
	}
	var b bytes.Buffer
	b.Write(bytes.TrimSuffix(base, []byte("}")))
	for _, k := range slices.Sorted(maps.Keys(r.Extra)) {
		if isRecordKey(k) {
			return nil, fmt.Errorf("game: run record field %q in Extra clashes with a field of its own", k)
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		// Marshalling the raw value checks that it is valid JSON and
		// compacts it, so one bad value can't corrupt the whole file.
		val, err := json.Marshal(r.Extra[k])
		if err != nil {
			return nil, fmt.Errorf("game: run record field %q: %w", k, err)
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads the named fields as usual and puts every other
// top-level key in Extra (nil when there are none). Extra is replaced, not
// merged, so a record read twice never keeps a stale adapter field. A
// JSON null changes nothing, as for any struct.
func (r *Record) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(data, (*recordFields)(r)); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	r.Extra = nil
	for k, v := range all {
		if isRecordKey(k) {
			continue
		}
		// Compact, so a value reads the same from an indented file
		// (runtime.json is written indented) as from a compact one, and
		// an adapter comparing raw bytes isn't fooled by spacing.
		var c bytes.Buffer
		if err := json.Compact(&c, v); err != nil {
			return err
		}
		if r.Extra == nil {
			r.Extra = map[string]json.RawMessage{}
		}
		r.Extra[k] = c.Bytes()
	}
	return nil
}
