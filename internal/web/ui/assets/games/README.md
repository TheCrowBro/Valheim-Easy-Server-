# Game modules for the control panel

Each game can have one module here, `<id>.js`, named after its game id
(`Info.ID` in `internal/games/<id>`). It holds the few parts of the panel
that only make sense for that game: extra lines in "How friends join", how
its log lines and feed entries look, where its worlds are kept, and any tab
it draws itself (Valheim's Mods tab). Everything else on a game's pages is
drawn by the shell (`../app.js` and the modules next to it) from the
status and the game's settings schema, the same way for every game.

The shell never names a game (`TestNoGameIDs` checks `../*.js`). It loads
every module with `import('./games/<id>.js')` for each game in the catalog
(`GET /api/catalog`), so adding a game needs no change to a shared list.
They are files of the app itself, so the panel's Content-Security-Policy
(`script-src 'self'`) allows them. **A game without a module works**: if
the file is missing or fails to load, the shell uses the defaults below
(every hook answers nothing), and the game's pages still show everything
the status and the schema give.

This file is the contract between the shell (stream UI) and the modules
(UIV for Valheim, each G stream for its game). It was set in phase 0'
(docs/dev/multigame/spec.md 6.1, and docs/dev/multigame/p0r3-decisions.md).
A change to it goes through the lead.

## The module

A module is an ES module whose default export is an object. Every key is
optional; leave out what the game doesn't need.

```js
export default {
  id: 'minecraft',
  joinExtras(game, h) { return null; },        // extra nodes in How friends join, or null
  logClass(text) { return null; },             // 'join' | 'leave' | 'save' | 'warn' | null
  feedIcons: { turned_away: 'user-x' },         // feed kind → icon name
  worldsHelp: 'Your worlds are in …',           // where to find worlds on PC/Mac
  uploadAccept: '.zip',
  worldCard(world, game, h) { return null; },   // extra controls on a world card (Valheim: difficulty, mods)
  tabs: {},                                     // replacement tab modules by tab id (Valheim: mods)
  checklist(game, h) { return null; },          // extra rows for the setup checklist on its Home
};
```

| Key | What the shell does with it | Default |
|---|---|---|
| `id` | Must equal the file's name and the game id; the shell ignores a module whose `id` differs. | — |
| `joinExtras(game, h)` | Called each time the game's "How friends join" card is drawn; the nodes it returns go at the end of the card. | nothing extra |
| `logClass(text)` | Called for each line on the Log tab (`text` is the line as shown); the answer becomes the line's style. | plain |
| `feedIcons` | Icons for the game's own feed kinds (below), by kind. A value is an icon name from the shell's set (`icon()` in `../dom.js`; stream UI adds one a module needs), or `[iconName, tone]` with tone `'good'`, `'bad'`, `'warn'`, `'accent'` or `''`, as the shell's own kinds have. | a kind the shell doesn't know shows as a warning |
| `worldsHelp` | Plain text under the Worlds tab's import: where the owner's own game keeps its worlds, on a PC and on a Mac. | none |
| `uploadAccept` | The file picker's `accept` for importing worlds (`'.zip'`, `'.zip,.wld'`). | `'.zip'` |
| `worldCard(world, game, h)` | Called for each world card on the Worlds tab; the nodes it returns go on the card (Valheim: its difficulty and mods). | nothing extra |
| `tabs` | Tabs the game draws itself, by tab id (`home worlds players stats mods settings log`). A tab listed here replaces the shell's own; the Mods tab is shown only when the game has mods (`Info.Mods`). | the shell's tabs |
| `checklist(game, h)` | Called when the game's setup checklist is drawn on its Home; it returns extra rows (an array of nodes) for things only this game needs ("The RCON port (25575) can be reached from your home network; it has a new random password every start.", Minecraft on a Mac). | nothing extra |

### The arguments

- `game` is that instance's entry in `GET /api/status`'s `games` list
  (spec 4.9, `GameStatus` in `internal/manager/status_games.go`): `id`,
  `game`, `state`, `join`, `players`, `ports`, `world`, `runningWorld` and the
  rest. It is a snapshot: don't keep it or change it.
- `world` is one entry of `GET /api/games/{id}/worlds`'s `worlds` list.
- `h(tag, props, ...children)` is the shell's element builder (`h` in
  `../dom.js`): `h('div', { class: 'muted' }, 'text', otherNode)`. Use it
  rather than `innerHTML`, so names players typed are never read as HTML.
- A module that needs more of the shell's toolkit (`icon`, `api`, `toast`,
  `copyText`, `openModal`, `field`, `toggle`) imports it from `'../dom.js'`,
  whose exports stream UI keeps as they are. A module never imports another
  game's module.

### Tab modules

A tab module in `tabs` has the same shape as the shell's own pages:

```js
{
  title: 'Mods',                    // the tab's label (optional: the shell's label otherwise)
  render(app, game) { return node; },   // the tab's content, drawn on every refresh
  mount(app, game) {},              // once, when the tab is opened (optional)
  unmount(app, game) {},            // when it is left (optional)
  afterRender(app, game) {},        // after each draw, e.g. for a chart (optional)
}
```

`app` is the shell's `app` object (`app.status`, `app.render()`,
`app.actions`). A tab module may be a separate file under `games/<id>/`
(`games/valheim/mods.js`) that `<id>.js` imports.

### Rules

- **Everything is per instance.** The same module serves every instance of
  its game (a second Minecraft server would be `minecraft-2`), and the shell
  reuses a tab when the owner switches games. Keep any state the module
  holds in a map keyed by `game.id`, never in one variable, so a tab never
  shows another game's data. The shell keeps each instance's log cursor and
  page state the same way.
- **Draw, don't act.** `joinExtras`, `logClass`, `worldCard` and `checklist`
  are called on every refresh (about every 2 seconds): they must be quick,
  draw only from their arguments, and never call the API. A button they
  draw does its work when it is clicked.
- **Plain words.** Texts follow the rest of the panel: short, plain English,
  saying what to do. Until stream T's rename lands, a module that names the
  app says "Valheim Easy Server".
- Every module passes `node --check` (CI checks every `.js` under
  `ui/assets`).

## Feed kinds

Each entry of the activity feed has a `kind`. The shell styles its own (and
a game's events of the shared kinds `online`, `join`, `leave`, `death`,
`saved`, `stopping`, `count`, `code`, `turned_away`: `internal/game`'s
`Kind…` constants). A game's Monitor may give events of its own kinds; the
manager shows any event that has text under its kind, and does nothing
else with it. The game's module gives those kinds an icon in `feedIcons`;
without one they show as warnings. Kinds the games use so far:

| Kind | Games | What it means |
|---|---|---|
| `turned_away` | Minecraft | someone not on the allow list was refused; the shell offers **Allow** (`POST /api/games/{id}/allow {name}`) |
| `allow_list_on` | Minecraft | the app turned the allow list on, because someone came in from the internet |
| `rcon_failed` | Minecraft, Project Zomboid | the app couldn't reach the server's RCON |
| `admin_prompt` | Project Zomboid | the server asked for an admin password |
| `wrong_password` | Valheim, Vintage Story | someone typed the wrong server password |
| `bad_version` | Valheim | someone joined with another version of the game |
| `bad_password_config` | Valheim | the server refused its password setting |
| `load_failed` | Valheim, Vintage Story, Enshrouded | the world couldn't be loaded |
| `save_failed` | Enshrouded | the world couldn't be saved |
| `server_full` | Terraria, Enshrouded | someone was refused because the server is full |
| `port_in_use`, `no_steam`, `bad_config` | Enshrouded | the server couldn't use its port, reach Steam, or read its settings |
