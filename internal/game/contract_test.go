package game

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/worlds"
)

// everything implements Server and every optional capability at once, as
// the Valheim adapter will implement most of them. If two interfaces ever
// wanted the same method name with different signatures, this would stop
// compiling, so no adapter could offer both.
type everything struct{}

var _ interface {
	Server
	Prober
	Lister
	OnlineTexter
	Afterer
	ModLister
	Remover
	Recoverer
} = everything{}

var (
	_ Game      = testGame{}
	_ Store     = memStore{}
	_ Installer = noInstall{}
	_ Monitor   = quietMonitor{}
	_ Worlds    = noWorlds{}
	_ error     = FieldErrors{}
)

type memStore struct{}

func (memStore) Get() Saved                    { return Saved{} }
func (memStore) Save(func(*Saved) error) error { return nil }

type noInstall struct{}

func (noInstall) Install(context.Context, bool, func(Progress)) error { return nil }

type quietMonitor struct{}

func (quietMonitor) Line(string, time.Time) Line           { return Line{} }
func (quietMonitor) Tick(time.Time) []Event                { return nil }
func (quietMonitor) Probed(ProbeResult, time.Time) []Event { return nil }
func (quietMonitor) State() RunState                       { return RunState{Count: -1} }
func (quietMonitor) End(time.Time) []Event                 { return nil }

type noWorlds struct{}

func (noWorlds) List() ([]worlds.World, []worlds.Orphan, error) { return nil, nil, nil }
func (noWorlds) Get(string) (worlds.World, error)               { return worlds.World{}, nil }
func (noWorlds) Exists(string) bool                             { return false }
func (noWorlds) NameProblem(string) string                      { return "" }
func (noWorlds) Backup(string, string) (worlds.Backup, error)   { return worlds.Backup{}, nil }
func (noWorlds) Restore(string, string) error                   { return nil }
func (noWorlds) Delete(string) error                            { return nil }
func (noWorlds) Rename(string, string) error                    { return ErrCantRename }
func (noWorlds) ExportFile(string, string) (string, error)      { return "", nil }
func (noWorlds) ExportBackup(io.Writer, string, string) error   { return nil }
func (noWorlds) Scan(string) []worlds.Candidate                 { return nil }
func (noWorlds) Local() []worlds.Candidate                      { return nil }
func (noWorlds) CleanStaging(time.Duration)                     {}
func (noWorlds) Import([]worlds.Candidate, []worlds.Choice, func(string) bool, bool) ([]worlds.Placed, error) {
	return nil, nil
}

func (everything) Store() Store                                    { return memStore{} }
func (everything) Program() string                                 { return "" }
func (everything) Installer() Installer                            { return noInstall{} }
func (everything) Build() (Build, bool)                            { return Build{}, false }
func (everything) Schema() Schema                                  { return nil }
func (everything) Validate(Saved, string) FieldErrors              { return nil }
func (everything) Plan(context.Context, PlanRequest) (Plan, error) { return Plan{}, nil }
func (everything) Watch(Record, func(string) string) Monitor       { return quietMonitor{} }
func (everything) Stop(Record) []StopStep                          { return nil }
func (everything) Join(JoinInput) Join                             { return Join{} }
func (everything) Worlds() Worlds                                  { return noWorlds{} }
func (everything) Texts() Texts                                    { return Texts{} }
func (everything) ExitHint([]string) string                        { return "" }
func (everything) Fingerprint(Saved, string) string                { return "" }

func (everything) Probe(context.Context, Record) (ProbeResult, error) { return ProbeResult{}, nil }
func (everything) Lists() []ListKind                                  { return nil }
func (everything) ReadList(string) ([]string, error)                  { return nil, nil }
func (everything) WriteList(context.Context, *Record, string, []string) error {
	return nil
}
func (everything) Online(OnlineInput) string { return "" }
func (everything) After(context.Context, Record) (map[string]json.RawMessage, error) {
	return nil, nil
}
func (everything) Pending(Record) bool                    { return false }
func (everything) PlayerMods(string, *Record) []mods.Info { return nil }
func (everything) ModsCode(*Record) string                { return "" }
func (everything) WorldMods() []mods.Info                 { return nil }
func (everything) SetWorldMods(string, []string) error    { return nil }
func (everything) Remove(context.Context) error           { return nil }
func (everything) Recover(int, time.Time) Record          { return Record{} }

// TestNamedValues pins the values that are stored, sent to the panel or
// compared across builds, so a rename of a constant can't change them.
func TestNamedValues(t *testing.T) {
	for got, want := range map[string]string{
		string(Hidden): "hidden", string(Preview): "preview", string(Ready): "ready",
		string(Runs): "yes", string(CantRun): "no",
		string(ByName): "names", string(ByCount): "count", string(Unknown): "none",
		string(ServerScope): "server", string(WorldScope): "world", string(NewWorldScope): "newWorld",
		ErrNoConsole.Error(): "no console", ErrCantRename.Error(): "can't rename",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if Capture != 0 || LogArg != 1 || GameLog != 2 {
		t.Errorf("Output values moved: %d %d %d", Capture, LogArg, GameLog)
	}
}

// TestJSONNames checks the JSON the panel will read: lower-case names, and
// nothing written for an empty optional field.
func TestJSONNames(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{Choice{Value: "normal", Label: "Normal"}, `{"value":"normal","label":"Normal"}`},
		{Build{}, `{}`},
		{Build{Version: "26.3", Date: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)}, `{"version":"26.3","date":"2026-11-26T00:00:00Z"}`},
		{PortRole{Role: "game", Label: "Game port", Protos: []string{"tcp"}, Default: 25565, Public: true},
			`{"role":"game","label":"Game port","protos":["tcp"],"default":25565,"public":true}`},
		{Field{Key: "difficulty", Scope: WorldScope, Kind: "choice", Label: "Difficulty", Choices: []Choice{{"easy", "Easy"}}},
			`{"key":"difficulty","scope":"world","kind":"choice","label":"Difficulty","choices":[{"value":"easy","label":"Easy"}]}`},
		{Player{Name: "Alex", Since: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)}, `{"name":"Alex","since":"2026-11-26T00:00:00Z"}`},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("%T:\n got %s\nwant %s", c.v, b, c.want)
		}
	}
}

func TestFieldErrors(t *testing.T) {
	var err error = FieldErrors{"world.seed": "Seeds are numbers.", "server.serverName": "Give the server a name."}
	if got, want := err.Error(), "Give the server a name. Seeds are numbers."; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (FieldErrors{}).Error(); got != "" {
		t.Errorf("no errors: %q", got)
	}
}
