package windrose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ServerDescription.json is the server's own settings file (FACTS.md,
// "ServerDescription.json"):
//
//	{
//		"Version": 1,
//		"DeploymentId": "0.10.0.9.32-22d39a16",
//		"ServerDescription_Persistent":
//		{
//			"PersistentServerId": "…", "InviteCode": "d6221bb7",
//			"IsPasswordProtected": false, "Password": "", "ServerName": "",
//			"WorldIslandId": "…", "MaxPlayerCount": 4, "UserSelectedRegion": "",
//			"P2pProxyAddress": "192.168.1.20", "UseDirectConnection": false, …
//		}
//	}
//
// The server makes it at its first start, reads it at every start and
// writes it back while it runs, so the app edits it only while the server
// is stopped (the publisher: "This file should only be modified while the
// server is shut down"). The app changes the keys it owns and keeps every
// other one exactly as it was, in its place: PersistentServerId above all,
// which is the server's identity with Windrose's online service (a server
// whose id changes loses its worlds' link to it).
const (
	persistentKey = "ServerDescription_Persistent"

	keyServerName  = "ServerName"
	keyPassword    = "Password"
	keyProtected   = "IsPasswordProtected"
	keyMaxPlayers  = "MaxPlayerCount"
	keyWorld       = "WorldIslandId"
	keyRegion      = "UserSelectedRegion"
	keyInviteCode  = "InviteCode"
	keyProxy       = "P2pProxyAddress"
	keyDirect      = "UseDirectConnection"
	keyServerID    = "PersistentServerId"
	keyDeployment  = "DeploymentId"
	byteOrderMark  = "\ufeff"
	descriptionEOL = "\r\n" // as the server writes it
)

// description is ServerDescription.json, read so that it can be written
// back with every key the app doesn't own unchanged and in its order.
type description struct {
	top  object
	pers object
}

// readDescription reads the file. It is an error if the file isn't the
// shape the publisher documents, so a start never runs on a file the app
// couldn't understand.
func readDescription(path string) (*description, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDescription(b)
}

func parseDescription(b []byte) (*description, error) {
	b = bytes.TrimPrefix(b, []byte(byteOrderMark))
	d := &description{}
	if err := json.Unmarshal(b, &d.top); err != nil {
		return nil, fmt.Errorf("ServerDescription.json isn't valid JSON: %w", err)
	}
	raw, ok := d.top.vals[persistentKey]
	if !ok {
		return nil, fmt.Errorf("ServerDescription.json has no %s section", persistentKey)
	}
	if err := json.Unmarshal(raw, &d.pers); err != nil {
		return nil, fmt.Errorf("ServerDescription.json's %s isn't an object: %w", persistentKey, err)
	}
	return d, nil
}

// bytes is the file as the app writes it: tab-indented with Windows line
// endings, as the server itself writes it.
func (d *description) bytes() ([]byte, error) {
	pers, err := marshal(d.pers)
	if err != nil {
		return nil, err
	}
	d.top.setRaw(persistentKey, pers)
	flat, err := marshal(d.top)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, flat, "", "\t"); err != nil {
		return nil, err
	}
	// The server puts an object's opening brace on a line of its own.
	s := reOpenBrace.ReplaceAllString(out.String(), "$1$2\n$1{")
	s = strings.ReplaceAll(s, "\n", descriptionEOL) + descriptionEOL
	return []byte(s), nil
}

// reOpenBrace is a key whose value is an object, as json.Indent writes it.
var reOpenBrace = regexp.MustCompile(`(?m)^(\t*)("(?:[^"\\]|\\.)*":) \{$`)

// write replaces the file in one step (a new file, then a rename), so the
// server never finds half of one.
func (d *description) write(path string) error {
	b, err := d.bytes()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ServerDescription-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// str is a text value of the persistent section ("" when it isn't text).
func (d *description) str(key string) string {
	var v string
	d.pers.get(key, &v)
	return v
}

// boolean is a true/false value of the persistent section.
func (d *description) boolean(key string) bool {
	var v bool
	d.pers.get(key, &v)
	return v
}

// version is the server's own version, from DeploymentId
// ("0.10.0.9.32-22d39a16" is version 0.10.0.9.32).
func (d *description) version() string {
	var v string
	d.top.get(keyDeployment, &v)
	v, _, _ = strings.Cut(v, "-")
	return v
}

// object is a JSON object that keeps its keys in the order they came in,
// with the values as they were written, so that writing it back changes
// only what the app set.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func (o *object) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	o.keys, o.vals = nil, map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("a key that isn't text")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, dup := o.vals[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.vals[key] = raw
	}
	_, err = dec.Token() // the closing brace
	return err
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// get decodes a value into v; false when the key isn't there or doesn't
// fit v.
func (o *object) get(key string, v any) bool {
	raw, ok := o.vals[key]
	return ok && json.Unmarshal(raw, v) == nil
}

// set changes a value, or adds the key at the end.
func (o *object) set(key string, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	o.setRaw(key, b)
	return nil
}

func (o *object) setRaw(key string, raw json.RawMessage) {
	if o.vals == nil {
		o.vals = map[string]json.RawMessage{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
}

// marshal writes a value as JSON, leaving <, > and & as they are: a
// password or a name may well have them, and the server reads them either
// way.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
