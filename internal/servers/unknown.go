package servers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Keys this build doesn't know are kept at every level of the file (the top,
// "app", each instance and its "discord"), so that an older R3.x build which
// changes the list (a star, a setting) writes back everything a newer build
// put there. One pair of helpers does it for every level, so the levels
// can't drift apart.

// The same types without their JSON methods, so encoding/json handles the
// named fields the usual way and the methods only add Unknown.
type (
	fileFields     File
	appFields      App
	instanceFields Instance
	discordFields  Discord
)

var (
	fileKeys     = jsonNames[fileFields]()
	appKeys      = jsonNames[appFields]()
	instanceKeys = jsonNames[instanceFields]()
	discordKeys  = jsonNames[discordFields]()
)

// jsonNames lists the JSON names of T's fields (T is a struct).
func jsonNames[T any]() []string {
	t := reflect.TypeFor[T]()
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-" || !f.IsExported():
			continue
		case name == "":
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// known says whether key is one of names as encoding/json matches keys to
// fields: ignoring case. Such a key is read into the field, so keeping it
// in Unknown as well would write it twice.
func known(names []string, key string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, key) })
}

// decodeKeeping reads data (a JSON object) into fields, a pointer to one of
// the method-less types above, and returns the keys fields has no place for
// (nil when there are none). Values are compacted, so a key reads the same
// from an indented file as from a compact one.
func decodeKeeping(data []byte, fields any, names []string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, fields); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var unknown map[string]json.RawMessage
	for k, v := range all {
		if known(names, k) {
			continue
		}
		var c bytes.Buffer
		if err := json.Compact(&c, v); err != nil {
			return nil, err
		}
		if unknown == nil {
			unknown = map[string]json.RawMessage{}
		}
		unknown[k] = c.Bytes()
	}
	return unknown, nil
}

// encodeKeeping writes fields as usual, then each unknown key, sorted so
// the file doesn't change from one write to the next. An unknown key that
// names a field is refused: reading the file back would put its value in
// that field, so it can only be a bug, and failing the write makes a test
// catch it rather than a friend's server losing a setting.
func encodeKeeping(fields any, names []string, unknown map[string]json.RawMessage) ([]byte, error) {
	base, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if len(unknown) == 0 {
		return base, nil
	}
	var b bytes.Buffer
	b.Write(bytes.TrimSuffix(base, []byte("}")))
	for _, k := range slices.Sorted(maps.Keys(unknown)) {
		if known(names, k) {
			return nil, fmt.Errorf("servers: kept key %q clashes with a field of the same name", k)
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		// Marshalling the raw value checks that it is valid JSON, so one
		// bad value can't make the whole file unreadable.
		val, err := json.Marshal(unknown[k])
		if err != nil {
			return nil, fmt.Errorf("servers: kept key %q: %w", k, err)
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

// isNull reports a JSON null, which leaves a struct as it was (as
// encoding/json does for any struct).
func isNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

// cloneRaw copies a kept-keys map, so a copy of a struct doesn't share it.
func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return nil
	}
	c := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		c[k] = bytes.Clone(v)
	}
	return c
}

// MarshalJSON writes the file with its kept keys. "instances" is written as
// [] rather than null when there are none, so the file reads plainly.
func (f File) MarshalJSON() ([]byte, error) {
	if f.Instances == nil {
		f.Instances = []Instance{}
	}
	return encodeKeeping(fileFields(f), fileKeys, f.Unknown)
}

// UnmarshalJSON reads the file and keeps the keys this build doesn't know.
// Unknown is replaced, not merged.
func (f *File) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	u, err := decodeKeeping(data, (*fileFields)(f), fileKeys)
	if err != nil {
		return err
	}
	f.Unknown = u
	return nil
}

// MarshalJSON writes the app settings with their kept keys. "stars" is
// written as [] rather than null when nothing is starred.
func (a App) MarshalJSON() ([]byte, error) {
	if a.Stars == nil {
		a.Stars = []string{}
	}
	return encodeKeeping(appFields(a), appKeys, a.Unknown)
}

// UnmarshalJSON reads the app settings and keeps the keys this build
// doesn't know.
func (a *App) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	u, err := decodeKeeping(data, (*appFields)(a), appKeys)
	if err != nil {
		return err
	}
	a.Unknown = u
	return nil
}

// MarshalJSON writes the instance with its kept keys.
func (in Instance) MarshalJSON() ([]byte, error) {
	return encodeKeeping(instanceFields(in), instanceKeys, in.Unknown)
}

// UnmarshalJSON reads the instance and keeps the keys this build doesn't
// know, so an instance of a game this build doesn't have, or one with
// settings a newer build added, is written back whole.
func (in *Instance) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	u, err := decodeKeeping(data, (*instanceFields)(in), instanceKeys)
	if err != nil {
		return err
	}
	in.Unknown = u
	return nil
}

// MarshalJSON writes the Discord rules with their kept keys.
func (d Discord) MarshalJSON() ([]byte, error) {
	return encodeKeeping(discordFields(d), discordKeys, d.Unknown)
}

// UnmarshalJSON reads the Discord rules and keeps the keys this build
// doesn't know.
func (d *Discord) UnmarshalJSON(data []byte) error {
	if isNull(data) {
		return nil
	}
	u, err := decodeKeeping(data, (*discordFields)(d), discordKeys)
	if err != nil {
		return err
	}
	d.Unknown = u
	return nil
}
