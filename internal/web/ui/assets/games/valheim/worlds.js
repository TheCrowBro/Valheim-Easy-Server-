// Valheim's Worlds tab: add worlds (the Add a world section), pick which
// world the server plays, create new ones with their difficulty and mods,
// download them, and manage backups.
//
// Moved from the old panel's Worlds page (worlds.js), with the same
// texts, buttons and dialogs. What changed:
// - it reads and changes one instance through the new panel's routes
//   (/api/games/{id}/worlds/...), and keeps what it loaded per instance;
// - a world's difficulty is saved as that world's settings ("preset",
//   "modifier.<name>", "key.<name>", spec 2.9) through /worlds/values;
// - a world's mods are saved through the old route (saveWorldMods says why);
// - starting the server takes the new start route, which checks memory.
//
// It also gives the contract's worldCard hook: a world's difficulty and
// mods, as they appear on its card here.

import { h, icon, api, toast, openModal, confirmDialog, fmtBytes, ago, dateTime, field } from '../../dom.js';
import { gameApi, perGame, busy, serverRunning, isLegacy, legacyStatus, modsOf, redrawer, scoped, startGame } from './common.js';
import { uploadSection, uploadStatus, recent, connect, listenForDrops } from './upload.js';
import { saveZip, downloads, today } from './download.js';
import { openModsHelp } from './modshelp.js';

export const PRESET_LABELS = {
  normal: 'Normal', casual: 'Casual', easy: 'Easy', hard: 'Hard', hardcore: 'Hardcore',
  immersive: 'Immersive', hammer: 'Hammer (build freely)',
};
const MOD_LABELS = {
  combat: 'Combat', deathpenalty: 'Death penalty', resources: 'Resources', raids: 'Raids', portals: 'Portals',
};
const VALUE_LABELS = {
  veryeasy: 'Very easy', easy: 'Easy', hard: 'Hard', veryhard: 'Very hard', casual: 'Casual', hardcore: 'Hardcore',
  muchless: 'Much less', less: 'Less', more: 'More', muchmore: 'Much more', most: 'Most', none: 'None',
};
const KEY_LABELS = {
  nobuildcost: 'No build cost', playerevents: 'Raids based on each player’s progress',
  passivemobs: 'Passive enemies', nomap: 'No map', fire: 'Fire hazard',
};
const KEY_HELP = {
  fire: 'Fire spreads to wood everywhere, not just in the Ashlands, so buildings can burn down.',
};
// The mods are shown in these groups, and say what players need.
const MOD_GROUPS = [['world', 'How the land is made'], ['play', 'Gameplay'], ['look', 'Look and feel']];
const PLAYERS_NEED = {
  required: 'Players need it to join',
  recommended: 'Players can join without it, but should have it',
  players: 'Only for players’ own games: the server doesn’t use it',
};

// The old page's lists, used when the app's answer has no world schema.
const MODIFIERS = {
  combat: ['veryeasy', 'easy', 'hard', 'veryhard'],
  deathpenalty: ['casual', 'veryeasy', 'easy', 'hard', 'hardcore'],
  resources: ['muchless', 'less', 'more', 'muchmore', 'most'],
  raids: ['none', 'muchless', 'less', 'more', 'muchmore'],
  portals: ['casual', 'hard', 'veryhard'],
};

// What the tab loaded and shows, per instance.
const W = perGame(() => ({ data: null, loadedAt: 0, loading: false, openMenu: null, lastJob: undefined, timer: null }));
// The shell's app, to redraw the page when something loaded here arrives.
// The Mods tab uses it too.
const view = redrawer();
export const worldsView = view;
let shown = null; // the instance whose Worlds tab is open

export function worldsOf(game) {
  return W(game).data;
}

export async function load(game, force) {
  const s = W(game);
  if (s.loading || (!force && Date.now() - s.loadedAt < 15000)) return;
  s.loading = true;
  try {
    s.data = normalise(await api(gameApi(game, '/worlds')));
    s.loadedAt = Date.now();
  } catch (e) {
    if (!s.data) toast(e.message, 'bad');
  } finally {
    s.loading = false;
  }
  view.render();
}

// normalise fills in what the old page's code reads: each world's options
// (the legacy game's answer has them as the old page had them; otherwise
// they are read back from the world's settings) and the mods catalog.
function normalise(d) {
  d.worlds = d.worlds || [];
  d.orphans = d.orphans || [];
  d.mods = d.mods || [];
  d.schema = d.schema || {};
  if (!d.options) {
    d.options = {};
    for (const [name, v] of Object.entries(d.values || {})) d.options[name] = fromValues(v);
  }
  return d;
}

// The world settings Valheim has, from the app's world schema: the modifier
// names with their choices, and the switch names. The app only takes these
// keys, so the difficulty dialog offers exactly them.
function worldFields(data) {
  const fields = (data && data.schema && data.schema.world) || [];
  const modifiers = {};
  const keys = [];
  for (const f of fields) {
    if (f.key.startsWith('modifier.')) modifiers[f.key.slice(9)] = (f.choices || []).map((c) => c.value).filter(Boolean);
    else if (f.key.startsWith('key.')) keys.push(f.key.slice(4));
  }
  if (!fields.length) return { modifiers: MODIFIERS, keys: Object.keys(KEY_LABELS) };
  return { modifiers, keys };
}

// fromValues reads a world's settings back as the old page's options.
function fromValues(v) {
  const o = { preset: v.preset || '', modifiers: {}, keys: [], mods: [] };
  for (const [k, val] of Object.entries(v)) {
    if (k.startsWith('modifier.') && val) o.modifiers[k.slice(9)] = val;
    else if (k.startsWith('key.') && val === 'true') o.keys.push(k.slice(4));
  }
  if (v.mods) o.mods = v.mods.split(',').map((x) => x.trim()).filter(Boolean);
  return o;
}

// toValues writes the difficulty dialog's choices as the world's settings.
// With all, every setting is given, an empty one too, so a choice taken
// back is cleared (the app merges what it is sent); a new world is sent
// only what was chosen.
function toValues(data, o, all) {
  const { modifiers, keys } = worldFields(data);
  const v = {};
  if (all || o.preset) v.preset = o.preset || '';
  for (const k of Object.keys(modifiers)) {
    const x = (o.modifiers || {})[k] || '';
    if (all || x) v['modifier.' + k] = x;
  }
  for (const k of keys) {
    const on = (o.keys || []).includes(k);
    if (all || on) v['key.' + k] = on ? 'true' : 'false';
  }
  return v;
}

// saveWorldMods saves which mods a world is played with. No route of the
// new panel takes them for Valheim yet: its world settings (the schema)
// have no "mods" field, so /worlds/values refuses one. So for the legacy
// instance this uses the old route, which always serves the legacy Valheim
// (spec I7) and is what the old Worlds page called. (Reported to the lead.)
export function saveWorldMods(game, name, mods) {
  if (isLegacy(game)) return api('/api/worlds/mods', { method: 'POST', body: { name, mods } });
  return api(gameApi(game, '/worlds/values'), { method: 'POST', body: { name, values: { mods: mods.join(',') } } });
}

export function optionsSummary(o) {
  if (!o) return '';
  const parts = [];
  if (o.preset) parts.push(`${PRESET_LABELS[o.preset] || o.preset} difficulty`);
  for (const [k, v] of Object.entries(o.modifiers || {})) if (v) parts.push(`${MOD_LABELS[k] || k}: ${VALUE_LABELS[v] || v}`);
  for (const k of o.keys || []) parts.push(KEY_LABELS[k] || k);
  return parts.join(' · ');
}

function worldCard(app, game, w) {
  const data = W(game).data;
  const selected = data.selected === w.name;
  const running = data.running === w.name;
  const isBusy = busy(game);
  const s = W(game);
  const opts = optionsSummary(data.options[w.name]);
  const close = (fn) => () => { s.openMenu = null; view.render(); fn(); };
  const playBtn = running ? null : h('button', { class: 'btn btn-primary btn-sm', type: 'button', disabled: isBusy, onclick: () => play(app, game, w.name) },
    icon('play'), serverRunning(game) ? 'Switch to this world' : 'Play this world');
  const menu = s.openMenu === w.name ? h('div', { class: 'menu-list', role: 'menu' },
    h('button', { role: 'menuitem', type: 'button', onclick: close(() => backupsModal(app, game, w.name)) }, icon('archive'), `Backups (${w.backups.length})`),
    h('button', { role: 'menuitem', type: 'button', onclick: close(() => optionsModal(app, game, w.name)) }, icon('gauge'), 'Difficulty'),
    w.format === 'classic' ? h('button', { role: 'menuitem', type: 'button', onclick: close(() => renameModal(game, w.name)) }, icon('edit'), 'Rename') : null,
    h('button', { role: 'menuitem', type: 'button', class: 'danger', onclick: () => deleteWorld(app, game, w.name) }, icon('trash'), 'Delete…')) : null;
  const r = recent(game);
  const fresh = r.names.includes(w.name) && Date.now() < r.until;
  return h('article', { class: `card world${selected ? ' selected' : ''}${fresh ? ' just-added' : ''}`, 'data-key': w.name, 'data-world': w.name },
    h('div', { class: 'row' },
      h('div', { class: 'world-name' }, w.name),
      h('span', { class: 'spacer' }),
      running ? h('span', { class: 'badge good' }, 'Running now') : selected ? h('span', { class: 'badge accent' }, 'Selected') : null),
    h('div', { class: 'row', style: { gap: '6px' } },
      w.format === 'classic' ? h('span', { class: 'badge', title: 'Saved by a Valheim version before 1.0. The server converts it the first time it starts this world.' }, 'Old format') : null,
      w.neverSaved ? h('span', { class: 'badge warn' }, 'Not generated yet') : null,
      w.shadowed ? h('span', { class: 'badge warn', title: 'An old-format copy with the same name is also in the folder; Valheim uses this one.' }, 'Has an old copy') : null),
    h('dl', { class: 'meta' },
      h('dt', null, 'Seed'), h('dd', { class: 'mono' }, w.seed || '–'),
      h('dt', null, 'Size'), h('dd', null, fmtBytes(w.size)),
      h('dt', null, 'Last saved'), h('dd', null, w.modified ? ago(w.modified) : '–'),
      opts ? h('dt', null, 'Difficulty') : null, opts ? h('dd', null, opts) : null),
    modsLine(app, game, w.name, isBusy),
    h('div', { class: 'actions' },
      playBtn,
      downloadButton(app, game, w),
      h('button', { class: 'btn btn-sm', type: 'button', disabled: isBusy, onclick: () => backupNow(app, game, w.name) }, icon('archive'), 'Back up'),
      h('div', { class: 'menu' },
        h('button', {
          class: 'btn btn-sm btn-icon', type: 'button', 'aria-label': `More for ${w.name}`, 'aria-expanded': s.openMenu === w.name ? 'true' : 'false',
          onclick: () => { s.openMenu = s.openMenu === w.name ? null : w.name; view.render(); },
        }, icon('more')),
        menu)));
}

// worldMods are the mods a world is played with, from the catalog.
function worldMods(data, name) {
  const ids = (data.options[name] || {}).mods || [];
  return (data.mods || []).filter((m) => ids.includes(m.id));
}

// modsLine lists a world's mods, with buttons to change them and to see
// what players need.
export function modsLine(app, game, name, isBusy) {
  const data = W(game).data;
  const on = worldMods(data, name);
  return h('div', { class: 'world-mods' },
    h('div', { class: 'grow' },
      h('div', { class: 'title' }, 'Mods'),
      h('div', { class: 'help' }, on.length ? on.map((m) => m.name).join(', ') : 'None: plain Valheim.'),
      on.length ? h('button', { class: 'link-button small', type: 'button', onclick: () => modHelp(app, game, name) }, 'Player instructions') : null),
    h('button', { class: 'btn btn-sm', type: 'button', disabled: isBusy, onclick: () => modsModal(app, game, name) },
      icon(on.length ? 'edit' : 'plus'), on.length ? 'Change' : 'Add mods'));
}

// modHelp shows players what a world's mods are and where to get them.
function modHelp(app, game, name) {
  const list = worldMods(W(game).data, name);
  const ids = (l) => (l || []).map((m) => m.id).sort().join();
  // The code is for the mods the running server started with: only show
  // it with a list of those.
  const { mods, code } = modsOf(game, app);
  const same = game.runningWorld === name && ids(mods) === ids(list);
  openModsHelp(list, same ? code : '');
}

// modChecklist is the list of mods to tick, grouped. For an existing world
// (named in existing), changing a mod that makes the land, or adds places
// when a world is made, warns first.
function modChecklist(data, selected, existing) {
  const boxes = [];
  const groups = MOD_GROUPS.map(([group, title]) => {
    const list = (data.mods || []).filter((m) => m.group === group);
    if (!list.length) return null;
    return h('fieldset', { class: 'mod-group' }, h('legend', null, title),
      h('div', { class: 'mod-options' }, list.map((m) => {
        const was = selected.includes(m.id);
        const warns = (m.newWorlds || m.newPlaces) && existing;
        const text = m.newWorlds
          ? (was ? `“${existing}” won’t load without ${m.name} if it was created with it. Only turn it off if the world was made without it.`
            : `Only for a world created with ${m.name}, for example in your own game. “${existing}” won’t load with it if it was made without it: to play ${m.name}, create a new world.`)
          : (was ? `If “${existing}” was created with ${m.name}, the places it added go missing without it.`
            : `“${existing}” already exists, so ${m.name} won’t add places to it: they’re added when a world is created. To explore them, create a new world with it.`);
        const warn = h('div', { class: 'callout warn small mod-warn', hidden: true }, icon('alert'), h('div', { class: 'body' }, text));
        const cb = h('input', { type: 'checkbox', checked: was });
        if (warns) cb.onchange = () => { warn.hidden = cb.checked === was; };
        boxes.push([m.id, cb]);
        return h('label', { class: 'mod-option', 'data-mod': m.id }, cb,
          h('span', { class: 'grow' },
            h('span', { class: 'mod-option-name' }, m.name, m.newWorlds ? h('span', { class: 'badge accent' }, 'New worlds') : null,
              m.newPlaces ? h('span', { class: 'badge' }, 'Best on new worlds') : null),
            h('small', null, m.short),
            h('small', { class: 'mod-option-players' }, PLAYERS_NEED[m.players] || ''),
            warns ? warn : null));
      })));
  });
  return { el: groups, value: () => boxes.filter(([, cb]) => cb.checked).map(([id]) => id) };
}

// modsModal changes the mods a world is played with.
export function modsModal(app, game, name) {
  const data = W(game).data;
  const before = (data.options[name] || {}).mods || [];
  const existing = data.worlds.some((w) => w.name === name);
  const list = modChecklist(data, before, existing ? name : '');
  const running = data.running === name;
  const forServer = (ids) => ids.filter((id) => { const m = data.mods.find((x) => x.id === id); return m && m.players !== 'players'; }).sort().join();
  scoped(openModal({
    title: `Mods for “${name}”`,
    wide: true,
    body: [
      h('p', { class: 'text-2' }, 'Tick the mods to play this world with. The server downloads them when it starts this world, and only uses them for this world.'),
      list.el,
    ],
    actions: [{ label: 'Cancel' }, {
      label: 'Save', kind: 'primary', icon: 'check', onClick: async () => {
        const ids = list.value();
        await saveWorldMods(game, name, ids);
        const restart = running && forServer(ids) !== forServer(before);
        toast(`Mods for “${name}” saved.${restart ? ' Restart the server to use them.' : ''}`, 'good');
        await load(game, true);
        view.poll();
        const added = W(game).data.mods.filter((m) => ids.includes(m.id) && !before.includes(m.id));
        if (added.length) setTimeout(() => openModsHelp(added), 50); // what to tell the players, once this window has closed
      },
    }],
  }));
}

function downloadButton(app, game, w) {
  const dl = downloads[`${game.id}:world:${w.name}`];
  if (!dl) {
    return h('button', {
      class: 'btn btn-sm', type: 'button', title: 'Save a copy of this world as a zip file, for example on a USB stick',
      onclick: () => download(app, game, w.name),
    }, icon('download'), 'Download');
  }
  const pct = dl.total ? Math.min(100, Math.round((dl.sent / dl.total) * 100)) : -1;
  return h('div', { class: 'dl-progress', role: 'status' },
    h('span', { class: 'small text-2' }, dl.phase === 'packing' ? 'Packing the world…' : pct >= 0 ? `Saving… ${pct}%` : `Saving… ${fmtBytes(dl.sent)}`),
    h('div', { class: `progress${pct < 0 ? ' indeterminate' : ''}` }, h('i', { style: { width: (pct < 0 ? 35 : pct) + '%' } })));
}

function pendingCard(app, game, name) {
  const data = W(game).data;
  return h('article', { class: 'card world selected', 'data-key': 'pending:' + name },
    h('div', { class: 'row' }, h('div', { class: 'world-name' }, name), h('span', { class: 'spacer' }), h('span', { class: 'badge accent' }, 'Selected')),
    h('p', { class: 'text-2' }, 'This world doesn’t exist yet. The server creates it, with a new random seed, the first time it starts.'),
    optionsSummary(data.options[name]) ? h('p', { class: 'small muted' }, 'Difficulty: ' + optionsSummary(data.options[name])) : null,
    modsLine(app, game, name, false),
    h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', type: 'button', onclick: () => optionsModal(app, game, name) }, icon('gauge'), 'Difficulty')));
}

function orphanSection(app, game) {
  const data = W(game).data;
  if (!data.orphans.length) return null;
  return [
    h('h2', { class: 'section-title' }, 'Deleted or old worlds'),
    h('p', { class: 'text-2 small', style: { marginBottom: '12px' } }, 'These only exist as backups. Restore one to bring the world back.'),
    h('div', { class: 'world-grid' }, data.orphans.map((o) => h('article', { class: 'card world', 'data-key': 'orphan:' + o.name },
      h('div', { class: 'world-name' }, o.name),
      h('p', { class: 'small muted' }, `${o.backups.length} backup${o.backups.length === 1 ? '' : 's'}, newest ${ago(o.backups[0].time)}`),
      h('div', { class: 'actions' },
        h('button', { class: 'btn btn-sm', type: 'button', onclick: () => backupsModal(app, game, o.name) }, icon('archive'), 'Backups & restore'))))),
  ];
}

// The contract's worldCard hook: the difficulty and mods lines of a world,
// from what this tab loaded for the instance (nothing before that).
export function worldCardExtras(world, game) {
  const data = W(game).data;
  if (!data || !world) return null;
  const opts = optionsSummary(data.options[world.name]);
  return h('div', { class: 'vh' },
    opts ? h('p', { class: 'small text-2' }, 'Difficulty: ' + opts) : null,
    modsLine(view.app, game, world.name, busy(game)));
}

export const worldsTab = {
  title: 'Worlds',
  render(app, game) {
    view.set(app);
    shown = game;
    const s = W(game);
    uploadStatus(game);
    // Load again when one of the instance's jobs finishes (an import, a
    // restore, a delete), as the old page did when a job was done.
    const job = game.job;
    const done = job && job.finished ? job.id : null;
    if (s.lastJob !== undefined && done && done !== s.lastJob) load(game, true);
    s.lastJob = done;
    if (!s.data) return h('div', { class: 'vh' }, h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading worlds…')));
    const data = s.data;
    const exists = data.worlds.some((w) => w.name === data.selected);
    const isBusy = busy(game);
    return h('div', { class: 'vh' },
      uploadSection(game),
      h('div', { class: 'row', style: { margin: '22px 0 16px' } },
        h('p', { class: 'text-2', style: { flex: '1 1 320px' } },
          'The server plays the ', h('b', null, 'selected'), ' world. Switching worlds restarts the server, and your other worlds stay safe here. ',
          h('b', null, 'Download'), ' saves a copy of a world, for example on a USB stick or in a cloud-drive folder.'),
        h('button', { class: 'btn btn-primary', type: 'button', disabled: isBusy, onclick: () => newWorldModal(app, game) }, icon('plus'), 'Create a new world'),
        h('button', { class: 'btn', type: 'button', disabled: isBusy, onclick: () => localImportModal(app, game) }, icon('folder'), 'Import from this PC')),
      h('div', { class: 'world-grid' },
        exists || !data.selected ? null : pendingCard(app, game, data.selected),
        data.worlds.map((w) => worldCard(app, game, w))),
      orphanSection(app, game),
      data.folder ? h('p', { class: 'small muted', style: { marginTop: '18px' } },
        'Worlds are stored in ', h('span', { class: 'mono' }, data.folder), ' ',
        h('a', { href: '#', onclick: (e) => { e.preventDefault(); openFolder(); } }, 'Open folder')) : null);
  },
  mount(app, game) {
    view.set(app);
    shown = game;
    connect({ redraw: () => view.render(), play: (name) => play(view.app, shown, name) });
    listenForDrops();
    const s = W(game);
    load(game, true);
    document.addEventListener('click', closeMenus);
    clearInterval(s.timer);
    s.timer = setInterval(() => load(game, false), 15000);
  },
  unmount(app, game) {
    const s = W(game);
    clearInterval(s.timer);
    document.removeEventListener('click', closeMenus);
    s.openMenu = null;
    shown = null;
  },
  afterRender(app, game) {
    // Show a world that was just added.
    const r = recent(game);
    if (r.scrolled || Date.now() > r.until || !r.names.length) return;
    const card = [...document.querySelectorAll('.vh [data-world]')].find((el) => el.dataset.world === r.names[0]);
    if (card) {
      r.scrolled = true;
      card.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  },
};

function closeMenus(e) {
  if (!shown) return;
  const s = W(shown);
  if (s.openMenu && !e.target.closest('.menu')) {
    s.openMenu = null;
    view.render();
  }
}

function findWorld(data, name) {
  return data.worlds.find((w) => w.name === name) || data.orphans.find((o) => o.name === name);
}

// openFolder opens the worlds folder on the server computer (the app's own
// route, as the old page used).
function openFolder() {
  api('/api/system/open-folder', { method: 'POST', body: { which: 'worlds' } }).catch((e) => toast(e.message, 'bad', 9000));
}

// ---------- actions ----------

// play makes name the selected world and starts the server with it, or
// switches the running server to it.
export async function play(app, game, name) {
  if (!game) return;
  const running = serverRunning(game);
  if (running) {
    const n = (game.players || []).length;
    const ok = await confirmDialog({
      title: `Switch to “${name}”?`,
      message: `The server saves the current world and restarts with “${name}”. ${n ? `The ${n} player${n === 1 ? '' : 's'} online will be disconnected for about a minute.` : 'It takes about a minute.'}`,
      confirm: 'Switch world', icon: 'refresh',
    });
    if (!ok) return;
  }
  try {
    await api(gameApi(game, '/worlds/select'), { method: 'POST', body: { name, restart: running } });
    const started = running || await startGame(game);
    if (started) toast(running ? `Switching to “${name}”…` : `Starting the server with “${name}”…`, 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  load(game, true);
  view.poll();
}

function download(app, game, name) {
  const st = legacyStatus(game, app);
  let note = 'There’s a note inside on how to play it on another PC.';
  if (W(game).data.running === name && st && st.lastSave) note = `It has the world as of its last save, ${ago(st.lastSave)}. ` + note;
  saveZip(`${game.id}:world:${name}`, gameApi(game, '/worlds/download?name=' + encodeURIComponent(name)), `${name} ${today()}.zip`, note, () => view.render());
}

async function backupNow(app, game, name) {
  try {
    await api(gameApi(game, '/worlds/backup'), { method: 'POST', body: { name } });
  } catch (e) { toast(e.message, 'bad', 9000); }
  view.poll();
}

async function deleteWorld(app, game, name) {
  const s = W(game);
  s.openMenu = null;
  if (name === s.data.selected) {
    toast('This is the selected world. Switch to another world before deleting it.', 'warn', 8000);
    return view.render();
  }
  view.render();
  const ok = await confirmDialog({
    title: `Delete “${name}”?`,
    message: 'The world is removed from the server. A backup copy is kept, so you can bring it back later from “Deleted or old worlds”.',
    confirm: 'Delete world', kind: 'danger', icon: 'trash',
  });
  if (!ok) return view.render();
  try { await api(gameApi(game, '/worlds/delete'), { method: 'POST', body: { name } }); } catch (e) { toast(e.message, 'bad', 9000); }
  view.poll();
}

function renameModal(game, name) {
  const input = h('input', { class: 'input', value: name, maxlength: 40 });
  scoped(openModal({
    title: `Rename “${name}”`,
    body: [field('New name', input, 'Letters, numbers and spaces work best.')],
    actions: [{ label: 'Cancel' }, {
      label: 'Rename', kind: 'primary', onClick: async () => {
        await api(gameApi(game, '/worlds/rename'), { method: 'POST', body: { name, newName: input.value.trim() } });
        toast('World renamed.', 'good');
        load(game, true);
      },
    }],
  }));
}

// ---------- world options ----------
function optionsForm(data, current) {
  const o = current || {};
  const { modifiers, keys: keyNames } = worldFields(data);
  const preset = h('select', { class: 'input' },
    h('option', { value: '' }, 'Keep the world’s own setting'),
    Object.entries(PRESET_LABELS).map(([v, l]) => h('option', { value: v, selected: o.preset === v }, l)));
  const mods = {};
  const modFields = Object.entries(modifiers).map(([k, values]) => {
    const sel = h('select', { class: 'input' },
      h('option', { value: '' }, 'Default'),
      values.map((v) => h('option', { value: v, selected: (o.modifiers || {})[k] === v }, VALUE_LABELS[v] || v)));
    mods[k] = sel;
    return field(MOD_LABELS[k] || k, sel);
  });
  const keys = {};
  const keyFields = keyNames.map((k) => {
    const cb = h('input', { type: 'checkbox', checked: (o.keys || []).includes(k) });
    keys[k] = cb;
    return h('label', { class: 'key-option' }, cb, h('span', null, KEY_LABELS[k] || k, KEY_HELP[k] ? h('small', null, KEY_HELP[k]) : null));
  });
  // Valheim keeps switches in the world, so the server takes one out again
  // by resetting the world's difficulty (see WorldOptions.Off).
  const resetNote = h('div', { class: 'callout info' }, icon('info'), h('div', { class: 'body small' },
    'Valheim keeps switches in the world, so to turn one off the server resets this world’s difficulty each time it starts, then applies what’s set here. Boss progress isn’t affected.'));
  const showReset = () => {
    const resetting = [...(o.keys || []), ...(o.off || [])].some((k) => keys[k] && !keys[k].checked);
    resetNote.hidden = !resetting;
    preset.options[0].textContent = resetting ? 'Normal' : 'Keep the world’s own setting';
  };
  for (const cb of Object.values(keys)) cb.onchange = showReset;
  showReset();
  const more = h('details', { open: Object.keys(o.modifiers || {}).length || (o.keys || []).length || (o.off || []).length ? true : null },
    h('summary', { style: { cursor: 'pointer', color: 'var(--text)' } }, 'More options'),
    h('div', { class: 'stack', style: { marginTop: '12px', gap: '12px' } },
      h('div', { class: 'field-row' }, modFields),
      h('div', { class: 'stack', style: { gap: '6px' } }, keyFields),
      resetNote));
  return {
    el: [field('Difficulty', preset, 'A preset sets several options at once; the options below override it.'), more],
    value() {
      const modifiers = {};
      for (const [k, sel] of Object.entries(mods)) if (sel.value) modifiers[k] = sel.value;
      return { preset: preset.value, modifiers, keys: Object.keys(keys).filter((k) => keys[k].checked) };
    },
  };
}

function optionsModal(app, game, name) {
  const data = W(game).data;
  const form = optionsForm(data, data.options[name]);
  scoped(openModal({
    title: `Difficulty for “${name}”`,
    body: [...form.el, h('p', { class: 'small' }, 'Used the next time the server starts this world. Valheim keeps these settings inside the world.')],
    actions: [{ label: 'Cancel' }, {
      label: 'Save', kind: 'primary', onClick: async () => {
        await api(gameApi(game, '/worlds/values'), { method: 'POST', body: { name, values: toValues(data, form.value(), true) } });
        toast('Difficulty saved.', 'good');
        load(game, true);
        view.poll();
      },
    }],
  }));
}

function newWorldModal(app, game) {
  const data = W(game).data;
  const name = h('input', { class: 'input', placeholder: 'e.g. Second Voyage', maxlength: 40 });
  const form = optionsForm(data, null);
  const running = serverRunning(game);
  const now = h('input', { type: 'checkbox', checked: !running });
  const modList = modChecklist(data, [], '');
  scoped(openModal({
    title: 'Create a new world',
    wide: true,
    body: [
      field('World name', name, 'Up to 40 characters. Avoid \\ / : * ? " < > |'),
      ...form.el,
      (data.mods || []).length ? h('details', { class: 'where new-world-mods' },
        h('summary', null, 'Mods (optional)'),
        h('p', { class: 'small text-2' }, 'Tick any mods to play this world with. Players install most of them too. You can change them later, but choose Riverheim and More World Locations now: Riverheim makes the land, and More World Locations adds its places when the world is made.'),
        modList.el) : null,
      h('label', { class: 'row', style: { gap: '8px', cursor: 'pointer', color: 'var(--text)' } }, now,
        running ? 'Restart the server and switch to it now' : 'Start the server with it now'),
      h('div', { class: 'callout info' }, icon('info'), h('div', { class: 'body small' },
        'The server picks a random seed. Want a particular seed? Create the world in Valheim on your own PC, then add it with ', h('b', null, 'Add a world'), ' at the top of this page.')),
    ],
    actions: [{ label: 'Cancel' }, {
      label: 'Create world', kind: 'primary', icon: 'plus', onClick: async () => {
        const world = name.value.trim();
        const mods = modList.value();
        const restart = running && now.checked;
        // The mods go in before the server restarts with the new world, so
        // its first start already has them.
        await api(gameApi(game, '/worlds/create'), { method: 'POST', body: { name: world, values: toValues(data, form.value(), false), restart: restart && !mods.length } });
        if (mods.length) {
          await saveWorldMods(game, world, mods);
          if (restart) await api(gameApi(game, '/worlds/select'), { method: 'POST', body: { name: world, restart: true } });
        }
        if (!running && now.checked) await startGame(game);
        toast(`“${world}” is now the selected world.`, 'good');
        load(game, true);
        view.poll();
        const chosen = data.mods.filter((m) => mods.includes(m.id));
        if (chosen.length) setTimeout(() => openModsHelp(chosen), 50); // what to tell the players
      },
    }],
  }));
}

// ---------- backups ----------
function backupsModal(app, game, name) {
  const data = W(game).data;
  const w = findWorld(data, name);
  if (!w) return;
  const runningNow = data.running === name;
  const rows = w.backups.length ? w.backups.map((b) => h('div', { class: 'list-row' },
    h('div', { class: 'grow' },
      h('div', { class: 'name', style: { whiteSpace: 'normal' } }, b.label),
      h('div', { class: 'sub' }, `${dateTime(b.time)} · ${fmtBytes(b.size)}${b.format === 'classic' ? ' · old format' : ''}`)),
    h('button', {
      class: 'btn btn-sm btn-icon', type: 'button', 'aria-label': 'Download this backup', title: 'Download',
      onclick: () => saveZip(`${game.id}:backup:${b.id}`, gameApi(game, `/worlds/backup-download?name=${encodeURIComponent(name)}&id=${encodeURIComponent(b.id)}`),
        `${name} backup ${today(b.time)}.zip`, '', () => {}),
    }, icon('download')),
    h('button', {
      class: 'btn btn-sm', type: 'button', disabled: runningNow, title: runningNow ? 'Stop the server first' : null,
      onclick: async () => {
        const ok = await confirmDialog({
          title: 'Restore this backup?',
          message: `“${name}” goes back to how it was on ${dateTime(b.time)}. ${data.worlds.some((x) => x.name === name) ? 'The current version is backed up first, so you can undo this.' : ''}`,
          confirm: 'Restore', icon: 'archive',
        });
        if (!ok) return;
        modal.close();
        try { await api(gameApi(game, '/worlds/restore'), { method: 'POST', body: { name, backupId: b.id } }); } catch (e) { toast(e.message, 'bad', 9000); }
        view.poll();
      },
    }, 'Restore'))) : [h('div', { class: 'empty' }, 'No backups yet.')];
  const modal = scoped(openModal({
    title: `Backups of “${name}”`,
    wide: true,
    body: [
      runningNow ? h('div', { class: 'callout warn' }, icon('info'), h('div', { class: 'body small' }, 'This world is running. Stop the server before restoring a backup.')) : null,
      h('p', { class: 'small' }, 'Valheim makes automatic backups while the server runs, and this app keeps a copy before anything is replaced or deleted.'),
      h('div', { class: 'list' }, rows),
    ],
    actions: [
      data.worlds.some((x) => x.name === name) ? {
        label: 'Back up now', icon: 'archive', onClick: async () => {
          await api(gameApi(game, '/worlds/backup'), { method: 'POST', body: { name } });
          view.poll();
        },
      } : null,
      { label: 'Close', kind: 'primary' },
    ].filter(Boolean),
  }));
}

// ---------- importing from this PC ----------
function candidateRows(cands) {
  // Worlds the server already has are skipped unless chosen.
  const state = cands.map((c) => ({
    c,
    action: c.problem || c.superseded || c.exists ? 'skip' : 'import',
    name: (c.exists && c.freeName) || c.name || '',
  }));
  const els = state.map((s) => {
    const c = s.c;
    const nameInput = h('input', { class: 'input', value: s.name, maxlength: 40, disabled: !c.renamable && !!c.name, placeholder: 'Name on the server' });
    nameInput.oninput = () => { s.name = nameInput.value; };
    const sel = h('select', { class: 'input', style: { maxWidth: '260px' } },
      c.problem ? null : h('option', { value: c.exists ? 'replace' : 'import', selected: s.action !== 'skip' }, c.exists ? 'Replace the world on the server' : 'Import'),
      c.exists && c.renamable && !c.problem ? h('option', { value: 'import' }, 'Import with a different name') : null,
      h('option', { value: 'skip', selected: s.action === 'skip' }, 'Skip'));
    sel.onchange = () => { s.action = sel.value; };
    return h('div', { class: 'cand' },
      h('div', { class: 'row' },
        h('b', { style: { color: 'var(--text)', fontSize: '16px' } }, c.name || 'Unnamed world'),
        c.format === 'classic' ? h('span', { class: 'badge' }, 'Old format') : h('span', { class: 'badge' }, 'Valheim 1.0'),
        c.exists ? h('span', { class: 'badge warn' }, 'Name already used') : null,
        h('span', { class: 'spacer' }),
        h('span', { class: 'small muted' }, `${fmtBytes(c.size)}${c.modified ? ' · saved ' + ago(c.modified) : ''}`)),
      c.source ? h('div', { class: 'small muted' }, 'From: ' + c.source) : null,
      c.seed ? h('div', { class: 'small' }, 'Seed: ', h('span', { class: 'mono' }, c.seed)) : null,
      c.note ? h('div', { class: 'small text-2' }, c.note) : null,
      c.superseded ? h('div', { class: 'small text-2' }, 'Another copy of this world in the list is used instead, so this one is skipped.') : null,
      c.problem ? h('div', { class: 'small', style: { color: 'var(--bad-text)' } }, c.problem) : null,
      c.exists ? h('div', { class: 'small text-2' }, c.inUse
        ? 'The server is playing this world right now: replacing it restarts the server. A backup of the current world is kept first.'
        : 'Replacing keeps a backup of the current world first.') : null,
      h('div', { class: 'row' }, sel, c.renamable || !c.name ? nameInput : null));
  });
  return {
    el: els,
    // Replacing keeps the world's own name; the name box is for importing it as another world.
    choices: () => state.map((s) => ({ id: s.c.id, action: s.action, name: s.action === 'replace' ? '' : s.name.trim() })),
  };
}

async function localImportModal(app, game) {
  let cands;
  try {
    cands = (await api(gameApi(game, '/local-worlds'))).candidates;
  } catch (e) {
    return toast(e.message, 'bad', 9000);
  }
  if (!cands.length) {
    return scoped(openModal({
      title: 'Import from this PC',
      body: [h('p', null, 'No Valheim worlds were found in this computer’s game saves.'),
        h('p', { class: 'small' }, 'This looks in the folders Valheim uses on this computer (and Steam Cloud saves). To bring a world from another PC, drop it on Add a world at the top of the Worlds page.')],
      actions: [{ label: 'Close', kind: 'primary' }],
    }));
  }
  const rows = candidateRows(cands);
  scoped(openModal({
    title: 'Import from this PC',
    wide: true,
    body: [h('p', null, 'These worlds were found in Valheim’s saves on this computer. They’re copied, so the originals stay where they are.'), ...rows.el],
    actions: [{ label: 'Cancel' }, {
      label: 'Import', kind: 'primary', icon: 'check', onClick: async () => {
        await api(gameApi(game, '/local-worlds/import'), { method: 'POST', body: { choices: rows.choices() } });
        toast('Copying the worlds…', 'info');
        view.poll();
      },
    }],
  }));
}
