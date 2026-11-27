package enshrouded

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// configName is the server's settings file, beside its program. The
// server reads it at start and writes it back when it stops (adding the
// keys it was missing, and the bans made in the game), so the app changes
// only the keys it owns and keeps every other one as it is (FACTS.md,
// "The settings file").
const configName = "enshrouded_server.json"

// userGroup is one of the server's user groups: a password and what the
// players who join with it may do.
type userGroup struct {
	Name                 string `json:"name"`
	Password             string `json:"password"`
	CanKickBan           bool   `json:"canKickBan"`
	CanAccessInventories bool   `json:"canAccessInventories"`
	CanEditWorld         bool   `json:"canEditWorld"`
	CanEditBase          bool   `json:"canEditBase"`
	CanExtendBase        bool   `json:"canExtendBase"`
	ReservedSlots        int    `json:"reservedSlots"`
}

// friendsGroup is the one group the app gives the server. Everyone who
// joins shares its password, so it can do everything friends playing
// together do (build, use chests, terraform, place and upgrade Flame
// Altars, without which nobody could make a base), but not kick or ban,
// which one friend could then do to all the others. canEditWorld is given
// outright: the server refuses a group that may edit bases but not the
// world (FACTS.md).
func friendsGroup(password string) userGroup {
	return userGroup{Name: "Friends", Password: password, CanAccessInventories: true,
		CanEditWorld: true, CanEditBase: true, CanExtendBase: true}
}

// configValues are the settings the app gives the server for one start.
type configValues struct {
	name, saveDir, logDir string
	port, slots           int
	textChat, voiceChat   bool
	preset                string // "Default", …, "Custom"
	password              string // the Friends group's
}

// keyOrder is the order of the keys in the server's own 1.0 file, which
// the app keeps so the file reads as the server writes it. Keys the file
// has beyond these follow in the order they were in.
var keyOrder = []string{"name", "password", "saveDirectory", "logDirectory", "ip", "queryPort", "slotCount", "tags",
	"voiceChatMode", "enableVoiceChat", "enableTextChat", "gameSettingsPreset", "gameSettings", "userGroups", "bannedAccounts"}

// set lists what the app writes, by key. A nil value takes the key out.
func (c configValues) set() map[string]any {
	return map[string]any{
		"name":          c.name,
		"saveDirectory": c.saveDir,
		"logDirectory":  c.logDir,
		// Every address of this computer: friends come over the internet
		// and the home network, and the app checks it on 127.0.0.1.
		"ip":                 "0.0.0.0",
		"queryPort":          c.port,
		"slotCount":          c.slots,
		"enableVoiceChat":    c.voiceChat,
		"enableTextChat":     c.textChat,
		"gameSettingsPreset": c.preset,
		"userGroups":         []userGroup{friendsGroup(c.password)},
		// The password the server took before its user groups (2024).
		// It is no longer used for joining, but a non-empty one makes the
		// server add a second group, "default", with that password; so
		// the app takes it out (nil removes the key).
		"password": nil,
	}
}

// pair is one top-level key of the file, with its value as written.
type pair struct {
	key string
	raw json.RawMessage
}

// readConfig reads the server's settings file as its top-level keys in
// their order. A file that isn't there gives none; one that isn't a JSON
// object gives an error.
func readConfig(path string) ([]pair, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) // a BOM, as Notepad may save it
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%s isn't a JSON object", filepath.Base(path))
	}
	var out []pair
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("%s has a key that isn't text", filepath.Base(path))
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, pair{key, raw})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("%s doesn't end where its object does", filepath.Base(path))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s has more after its object", filepath.Base(path))
	}
	return out, nil
}

// renderConfig is the file with the app's keys set on top of old (the
// file's keys as they were), laid out with tabs as the server lays it out.
func renderConfig(old []pair, set map[string]any) ([]byte, error) {
	have := map[string]json.RawMessage{}
	for _, p := range old {
		if _, dup := have[p.key]; !dup {
			have[p.key] = p.raw
		}
	}
	var keys []string
	seen := map[string]bool{}
	add := func(k string) {
		if seen[k] {
			return
		}
		v, ours := set[k]
		_, kept := have[k]
		if (ours && v == nil) || (!ours && !kept) {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	for _, k := range keyOrder {
		add(k)
	}
	for _, p := range old {
		add(p.key)
	}
	var rest []string
	for k := range set {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}

	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range keys {
		var raw []byte
		if v, ours := set[k]; ours {
			enc, err := marshalNoEscape(v)
			if err != nil {
				return nil, err
			}
			raw = enc
		} else {
			raw = have[k]
		}
		var ind bytes.Buffer
		if err := json.Indent(&ind, raw, "\t", "\t"); err != nil {
			return nil, err
		}
		kb, _ := marshalNoEscape(k)
		b.WriteString("\t")
		b.Write(kb)
		b.WriteString(": ")
		b.Write(ind.Bytes())
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// marshalNoEscape is json.Marshal without turning <, > and & into \u
// escapes: the file is read by people too, and a server named "Tom & Ana"
// should read so.
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// writeConfig sets the app's keys in the server's settings file, keeping
// the rest. A file the server couldn't read either (it isn't JSON) is kept
// beside the new one as enshrouded_server.json.unreadable-<time>, so bans
// and hand-made settings in it aren't lost, and a new file is written.
func (s *server) writeConfig(c configValues) error {
	path := filepath.Join(s.env.Dirs.Server, configName)
	old, err := readConfig(path)
	if err != nil {
		keep := fmt.Sprintf("%s.unreadable-%s", path, time.Now().Format("2006-01-02_15-04-05"))
		if rerr := os.Rename(path, keep); rerr != nil {
			return fmt.Errorf("Enshrouded's settings file %s can't be read (%v) or moved aside (%v)", path, err, rerr)
		}
		s.log.Printf("%s couldn't be read (%v); it was kept as %s and a new one written", configName, err, keep)
		old = nil
	}
	b, err := renderConfig(old, c.set())
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

// writeFileAtomic writes the file whole or not at all: to a new file
// beside it, then renamed over it, so the server never reads half a file.
// It is written 0600 because it holds the friends' password.
func writeFileAtomic(path string, b []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, perm)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	return nil
}
