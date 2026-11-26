package valheim

import (
	"encoding/json"

	"valheim-easy-server/internal/game"
)

// Valheim's own fields in its run record. They are kept at the top level
// of runtime.json under the names older builds use, and only when set
// (as their omitempty did), so the file stays the same shape for a build
// taking a running server over, newer or older (invariant I4).
const (
	keyNoCrossplay = "noCrossplay" // started without crossplay, so no join code comes
	keyModsCode    = "modsCode"    // imports the running world's mods in r2modman or Gale
	keyCodeMods    = "codeMods"    // the mods (sorted ids) the code was made for
)

func extra[T any](rec game.Record, key string) T {
	var v T
	if raw, ok := rec.Extra[key]; ok {
		json.Unmarshal(raw, &v)
	}
	return v
}

// setExtra sets key to v, or removes it when v is its zero value.
func setExtra(rec *game.Record, key string, v any, zero bool) {
	if zero {
		delete(rec.Extra, key)
		if len(rec.Extra) == 0 {
			rec.Extra = nil
		}
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if rec.Extra == nil {
		rec.Extra = map[string]json.RawMessage{}
	}
	rec.Extra[key] = b
}

// NoCrossplay reports a run started without crossplay.
func NoCrossplay(rec game.Record) bool { return extra[bool](rec, keyNoCrossplay) }

// ModsCode is the mods code a run's record holds.
func ModsCode(rec game.Record) string { return extra[string](rec, keyModsCode) }

// CodeMods are the mods a run's mods code is for.
func CodeMods(rec game.Record) []string { return extra[[]string](rec, keyCodeMods) }

func setNoCrossplay(rec *game.Record, v bool) { setExtra(rec, keyNoCrossplay, v, !v) }

// SetModsCode records a run's mods code.
func SetModsCode(rec *game.Record, code string) { setExtra(rec, keyModsCode, code, code == "") }

func setCodeMods(rec *game.Record, ids []string) { setExtra(rec, keyCodeMods, ids, len(ids) == 0) }

// RecordExtra builds the Valheim fields of a run record as runtime.json
// holds them, for tests that write a record by hand.
func RecordExtra(noCrossplay bool, modsCode string, codeMods []string) map[string]json.RawMessage {
	var rec game.Record
	setNoCrossplay(&rec, noCrossplay)
	SetModsCode(&rec, modsCode)
	setCodeMods(&rec, codeMods)
	return rec.Extra
}
