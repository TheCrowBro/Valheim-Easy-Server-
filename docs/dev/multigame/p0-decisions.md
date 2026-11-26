# Phase 0: the lead's decisions on the frozen contracts

P0 (branch mg/p0) froze the contracts in internal/game and stubbed the new packages. These choices it made where the spec was silent or inconsistent are accepted, and are now part of the contract (spec section 2 and this file together):

1. `game.Choice` has JSON names `value` and `label` (positional literals still compile).
2. `internal/rcon`: `Dial(ctx, addr, password) (*Client, error)`, `(*Client).Exec(ctx, cmd) (string, error)`, `(*Client).Close() error`, one-shot `Exec(ctx, addr, password, cmd) (string, error)`, `ErrAuth`; for tests `TestServer{Addr}`, `NewTestServer(addr, password string, handle func(cmd string) string) (*TestServer, error)`, `(*TestServer).Close()`.
3. `internal/a2s`: `Info{Name, Map, Folder, Game, AppID, Players, MaxPlayers, Bots, Version}`, `Query(ctx, addr) (Info, error)` (2 s when ctx has no deadline); for tests `TestResponder{Addr}`, `NewTestResponder(addr string, challenge bool, info func() Info) (*TestResponder, error)`, `(*TestResponder).Close()`.
4. `install.Fake` (owned by stream F, internal/install/fake.go): `func Fake(f *game.Fake, program string, files []string) game.Installer`.
5. Additions: `Saved.Clone()`; registry rules: nil game refused; id matches `^[a-z][a-z0-9]{0,31}$`; the effective slash command (Command, or the id) valid and unique; Stage is hidden, preview or ready; `All()` breaks ties in Order by id; `Legacy()` is nil when nothing is registered.
6. Import rules: internal/manager and internal/web (those packages; subpackages such as managertest are exempt) import nothing under internal/games, including games/all; main.go imports games/all; internal/games/all imports only internal/game and adapters; adapters may import the standard library, third-party modules, their own subpackages and the listed internal packages, but not game/gametest.
7. TestNoGameIDs checks the 11 decided ids plus whatever is registered. TestNoTunnel matches the three words in any case, skipping docs/dev, hidden folders except .github, dist/data/dev-data, binaries and its own file.
8. Platform stubs return errors.ErrUnsupported; stream P may switch to platform's own ErrUnsupported. Stub defaults: GameFirewallReady false, RunElevatedWait -1, PortInUse/KnownFolder/ProcName "don't know".
9. Signature tests in install, worldstore, rcon, a2s, gametest and platform pin the stubs; an owner who changes a signature updates that test.
10. Record writes runtime.json's key set and values exactly; key order may differ (extra keys sorted after `password`). internal/manager/testdata/legacy/runtime.json stays where it is (the game package reads it).
