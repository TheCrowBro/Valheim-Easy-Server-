// Worlds: add worlds (the Add a world section), pick which world the server
// plays, create new ones, download them, and manage backups.

import { h, icon, api, toast, openModal, confirmDialog, fmtBytes, ago, dateTime, field } from './dom.js';
import { uploadSection, recent } from './upload.js';
import { saveZip, downloads, today } from './download.js';

let data = null;
let loadedAt = 0;
let loading = false;
let openMenu = null;
let appRef = null;

const PRESET_LABELS = {
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
const MODIFIERS = {
  combat: ['veryeasy', 'easy', 'hard', 'veryhard'],
  deathpenalty: ['casual', 'veryeasy', 'easy', 'hard', 'hardcore'],
  resources: ['muchless', 'less', 'more', 'muchmore', 'most'],
  raids: ['none', 'muchless', 'less', 'more', 'muchmore'],
  portals: ['casual', 'hard', 'veryhard'],
};

async function load(force) {
  if (loading || (!force && Date.now() - loadedAt < 15000)) return;
  loading = true;
  try {
    data = await api('/api/worlds');
    loadedAt = Date.now();
  } catch (e) {
    if (!data) toast(e.message, 'bad');
  } finally {
    loading = false;
  }
  if (appRef && appRef.page.id === 'worlds') appRef.render();
}

function optionsSummary(o) {
  if (!o) return '';
  const parts = [];
  if (o.preset) parts.push(`${PRESET_LABELS[o.preset] || o.preset} difficulty`);
  for (const [k, v] of Object.entries(o.modifiers || {})) if (v) parts.push(`${MOD_LABELS[k] || k}: ${VALUE_LABELS[v] || v}`);
  for (const k of o.keys || []) parts.push(KEY_LABELS[k] || k);
  return parts.join(' · ');
}

function serverRunning(app) {
  const s = app.status && app.status.state;
  return s === 'online' || s === 'starting';
}

function worldCard(app, w) {
  const selected = data.selected === w.name;
  const running = data.running === w.name;
  const busy = app.status && app.status.job && !app.status.job.finished;
  const opts = optionsSummary(data.options[w.name]);
  const playBtn = running ? null : h('button', { class: 'btn btn-primary btn-sm', 'data-action': 'world-play', 'data-name': w.name, disabled: busy },
    icon('play'), serverRunning(app) ? 'Switch to this world' : 'Play this world');
  const menu = openMenu === w.name ? h('div', { class: 'menu-list', role: 'menu' },
    h('button', { role: 'menuitem', 'data-action': 'world-backups', 'data-name': w.name }, icon('archive'), `Backups (${w.backups.length})`),
    h('button', { role: 'menuitem', 'data-action': 'world-options', 'data-name': w.name }, icon('gauge'), 'Difficulty'),
    w.format === 'classic' ? h('button', { role: 'menuitem', 'data-action': 'world-rename', 'data-name': w.name }, icon('edit'), 'Rename') : null,
    h('button', { role: 'menuitem', class: 'danger', 'data-action': 'world-delete', 'data-name': w.name }, icon('trash'), 'Delete…')) : null;
  const fresh = recent.names.includes(w.name) && Date.now() < recent.until;
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
    h('div', { class: 'actions' },
      playBtn,
      downloadButton(w),
      h('button', { class: 'btn btn-sm', 'data-action': 'world-backup', 'data-name': w.name, disabled: busy }, icon('archive'), 'Back up'),
      h('div', { class: 'menu' },
        h('button', { class: 'btn btn-sm btn-icon', 'data-action': 'world-menu', 'data-name': w.name, 'aria-label': `More for ${w.name}`, 'aria-expanded': openMenu === w.name ? 'true' : 'false' }, icon('more')),
        menu)));
}

function downloadButton(w) {
  const dl = downloads['world:' + w.name];
  if (!dl) {
    return h('button', { class: 'btn btn-sm', 'data-action': 'world-download', 'data-name': w.name, title: 'Save a copy of this world as a zip file, for example on a USB stick' },
      icon('download'), 'Download');
  }
  const pct = dl.total ? Math.min(100, Math.round((dl.sent / dl.total) * 100)) : -1;
  return h('div', { class: 'dl-progress', role: 'status' },
    h('span', { class: 'small text-2' }, dl.phase === 'packing' ? 'Packing the world…' : pct >= 0 ? `Saving… ${pct}%` : `Saving… ${fmtBytes(dl.sent)}`),
    h('div', { class: `progress${pct < 0 ? ' indeterminate' : ''}` }, h('i', { style: { width: (pct < 0 ? 35 : pct) + '%' } })));
}

function pendingCard(name) {
  return h('article', { class: 'card world selected', 'data-key': 'pending:' + name },
    h('div', { class: 'row' }, h('div', { class: 'world-name' }, name), h('span', { class: 'spacer' }), h('span', { class: 'badge accent' }, 'Selected')),
    h('p', { class: 'text-2' }, 'This world doesn’t exist yet. The server creates it, with a new random seed, the first time it starts.'),
    optionsSummary(data.options[name]) ? h('p', { class: 'small muted' }, 'Difficulty: ' + optionsSummary(data.options[name])) : null,
    h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'world-options', 'data-name': name }, icon('gauge'), 'Difficulty')));
}

function orphanSection(app) {
  if (!data.orphans.length) return null;
  return [
    h('h2', { class: 'section-title' }, 'Deleted or old worlds'),
    h('p', { class: 'text-2 small', style: { marginBottom: '12px' } }, 'These only exist as backups. Restore one to bring the world back.'),
    h('div', { class: 'world-grid' }, data.orphans.map((o) => h('article', { class: 'card world', 'data-key': 'orphan:' + o.name },
      h('div', { class: 'world-name' }, o.name),
      h('p', { class: 'small muted' }, `${o.backups.length} backup${o.backups.length === 1 ? '' : 's'}, newest ${ago(o.backups[0].time)}`),
      h('div', { class: 'actions' },
        h('button', { class: 'btn btn-sm', 'data-action': 'world-backups', 'data-name': o.name, 'data-orphan': '1' }, icon('archive'), 'Backups & restore'))))),
  ];
}

export default {
  id: 'worlds',
  title: 'Worlds',
  icon: 'globe',
  render(app) {
    appRef = app;
    if (!data) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading worlds…'));
    const exists = data.worlds.some((w) => w.name === data.selected);
    const busy = app.status && app.status.job && !app.status.job.finished;
    return h('div', null,
      uploadSection(app),
      h('div', { class: 'row', style: { margin: '22px 0 16px' } },
        h('p', { class: 'text-2', style: { flex: '1 1 320px' } },
          'The server plays the ', h('b', null, 'selected'), ' world. Switching worlds restarts the server, and your other worlds stay safe here. ',
          h('b', null, 'Download'), ' saves a copy of a world, for example on a USB stick or in a cloud-drive folder.'),
        h('button', { class: 'btn', 'data-action': 'world-new', disabled: busy }, icon('plus'), 'New world'),
        h('button', { class: 'btn', 'data-action': 'world-local', disabled: busy }, icon('folder'), 'Import from this PC')),
      h('div', { class: 'world-grid' },
        exists ? null : pendingCard(data.selected),
        data.worlds.map((w) => worldCard(app, w))),
      orphanSection(app),
      data.folder ? h('p', { class: 'small muted', style: { marginTop: '18px' } },
        'Worlds are stored in ', h('span', { class: 'mono' }, data.folder), ' ',
        h('a', { href: '#', 'data-action': 'open-folder', 'data-which': 'worlds' }, 'Open folder')) : null);
  },
  mount(app) {
    appRef = app;
    load(true);
    registerActions(app);
    document.addEventListener('click', closeMenus);
    this.timer = setInterval(() => load(false), 15000);
  },
  unmount() {
    clearInterval(this.timer);
    document.removeEventListener('click', closeMenus);
    openMenu = null;
  },
  onJobDone() { load(true); },
  afterRender() {
    // Show a world that was just added.
    if (recent.scrolled || Date.now() > recent.until || !recent.names.length) return;
    const card = [...document.querySelectorAll('[data-world]')].find((el) => el.dataset.world === recent.names[0]);
    if (card) {
      recent.scrolled = true;
      card.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  },
};

function closeMenus(e) {
  if (openMenu && !e.target.closest('.menu')) {
    openMenu = null;
    appRef.render();
  }
}

function findWorld(name) {
  return data.worlds.find((w) => w.name === name) || data.orphans.find((o) => o.name === name);
}

function registerActions(app) {
  const A = app.actions;
  A['world-menu'] = (d) => { openMenu = openMenu === d.name ? null : d.name; app.render(); };
  A['world-play'] = async (d) => {
    const running = serverRunning(app);
    if (running) {
      const n = app.status.players.length;
      const ok = await confirmDialog({
        title: `Switch to “${d.name}”?`,
        message: `The server saves the current world and restarts with “${d.name}”. ${n ? `The ${n} player${n === 1 ? '' : 's'} online will be disconnected for about a minute.` : 'It takes about a minute.'}`,
        confirm: 'Switch world', icon: 'refresh',
      });
      if (!ok) return;
    }
    try {
      await api('/api/worlds/select', { method: 'POST', body: { name: d.name, restart: running } });
      if (!running) await api('/api/server/start', { method: 'POST', body: {} });
      toast(running ? `Switching to “${d.name}”…` : `Starting the server with “${d.name}”…`, 'good');
    } catch (e) {
      toast(e.message, 'bad', 9000);
    }
    load(true);
    app.poll();
  };
  A['world-download'] = (d) => {
    const st = app.status;
    let note = 'There’s a note inside on how to play it on another PC.';
    if (data.running === d.name && st && st.lastSave) note = `It has the world as of its last save, ${ago(st.lastSave)}. ` + note;
    saveZip('world:' + d.name, '/api/worlds/download?name=' + encodeURIComponent(d.name), `${d.name} ${today()}.zip`, note, () => app.render());
  };
  A['world-backup'] = async (d) => {
    try {
      await api('/api/worlds/backup', { method: 'POST', body: { name: d.name } });
    } catch (e) { toast(e.message, 'bad', 9000); }
    app.poll();
  };
  A['world-delete'] = async (d) => {
    openMenu = null;
    if (d.name === data.selected) {
      toast('This is the selected world. Switch to another world before deleting it.', 'warn', 8000);
      return app.render();
    }
    const ok = await confirmDialog({
      title: `Delete “${d.name}”?`,
      message: 'The world is removed from the server. A backup copy is kept, so you can bring it back later from “Deleted or old worlds”.',
      confirm: 'Delete world', kind: 'danger', icon: 'trash',
    });
    if (!ok) return app.render();
    try { await api('/api/worlds/delete', { method: 'POST', body: { name: d.name } }); } catch (e) { toast(e.message, 'bad', 9000); }
    app.poll();
  };
  A['world-rename'] = (d) => {
    openMenu = null;
    app.render();
    const input = h('input', { class: 'input', value: d.name, maxlength: 40 });
    openModal({
      title: `Rename “${d.name}”`,
      body: [field('New name', input, 'Letters, numbers and spaces work best.')],
      actions: [{ label: 'Cancel' }, {
        label: 'Rename', kind: 'primary', onClick: async () => {
          await api('/api/worlds/rename', { method: 'POST', body: { name: d.name, newName: input.value.trim() } });
          toast('World renamed.', 'good');
          load(true);
        },
      }],
    });
  };
  A['world-options'] = (d) => { openMenu = null; app.render(); optionsModal(app, d.name); };
  A['world-backups'] = (d) => { openMenu = null; app.render(); backupsModal(app, d.name); };
  A['world-new'] = () => newWorldModal(app);
  A['world-local'] = () => localImportModal(app);
}

// ---------- world options ----------
function optionsForm(current) {
  const o = current || {};
  const preset = h('select', { class: 'input' },
    h('option', { value: '' }, 'Keep the world’s own setting'),
    Object.entries(PRESET_LABELS).map(([v, l]) => h('option', { value: v, selected: o.preset === v }, l)));
  const mods = {};
  const modFields = Object.entries(MODIFIERS).map(([k, values]) => {
    const sel = h('select', { class: 'input' },
      h('option', { value: '' }, 'Default'),
      values.map((v) => h('option', { value: v, selected: (o.modifiers || {})[k] === v }, VALUE_LABELS[v] || v)));
    mods[k] = sel;
    return field(MOD_LABELS[k], sel);
  });
  const keys = {};
  const keyFields = Object.entries(KEY_LABELS).map(([k, label]) => {
    const cb = h('input', { type: 'checkbox', checked: (o.keys || []).includes(k) });
    keys[k] = cb;
    return h('label', { class: 'key-option' }, cb, h('span', null, label, KEY_HELP[k] ? h('small', null, KEY_HELP[k]) : null));
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

function optionsModal(app, name) {
  const form = optionsForm(data.options[name]);
  openModal({
    title: `Difficulty for “${name}”`,
    body: [...form.el, h('p', { class: 'small' }, 'Used the next time the server starts this world. Valheim keeps these settings inside the world.')],
    actions: [{ label: 'Cancel' }, {
      label: 'Save', kind: 'primary', onClick: async () => {
        await api('/api/worlds/options', { method: 'POST', body: { name, options: form.value() } });
        toast('Difficulty saved.', 'good');
        load(true);
        app.poll();
      },
    }],
  });
}

function newWorldModal(app) {
  const name = h('input', { class: 'input', placeholder: 'e.g. Second Voyage', maxlength: 40 });
  const form = optionsForm(null);
  const running = serverRunning(app);
  const now = h('input', { type: 'checkbox', checked: !running });
  openModal({
    title: 'New world',
    body: [
      field('World name', name, 'Up to 40 characters. Avoid \\ / : * ? " < > |'),
      ...form.el,
      h('label', { class: 'row', style: { gap: '8px', cursor: 'pointer', color: 'var(--text)' } }, now,
        running ? 'Restart the server and switch to it now' : 'Start the server with it now'),
      h('div', { class: 'callout info' }, icon('info'), h('div', { class: 'body small' },
        'The server picks a random seed. Want a particular seed? Create the world in Valheim on your own PC, then add it with ', h('b', null, 'Add a world'), ' at the top of this page.')),
    ],
    actions: [{ label: 'Cancel' }, {
      label: 'Create world', kind: 'primary', icon: 'plus', onClick: async () => {
        await api('/api/worlds/create', { method: 'POST', body: { name: name.value.trim(), options: form.value(), restart: running && now.checked } });
        if (!running && now.checked) await api('/api/server/start', { method: 'POST', body: {} });
        toast(`“${name.value.trim()}” is now the selected world.`, 'good');
        load(true);
        app.poll();
      },
    }],
  });
}

// ---------- backups ----------
function backupsModal(app, name) {
  const w = findWorld(name);
  if (!w) return;
  const runningNow = data.running === name;
  const rows = w.backups.length ? w.backups.map((b) => h('div', { class: 'list-row' },
    h('div', { class: 'grow' },
      h('div', { class: 'name', style: { whiteSpace: 'normal' } }, b.label),
      h('div', { class: 'sub' }, `${dateTime(b.time)} · ${fmtBytes(b.size)}${b.format === 'classic' ? ' · old format' : ''}`)),
    h('button', {
      class: 'btn btn-sm btn-icon', 'aria-label': 'Download this backup', title: 'Download',
      onclick: () => saveZip('backup:' + b.id, `/api/worlds/backup-download?name=${encodeURIComponent(name)}&id=${encodeURIComponent(b.id)}`,
        `${name} backup ${today(b.time)}.zip`, '', () => {}),
    }, icon('download')),
    h('button', {
      class: 'btn btn-sm', disabled: runningNow, title: runningNow ? 'Stop the server first' : null,
      onclick: async () => {
        const ok = await confirmDialog({
          title: 'Restore this backup?',
          message: `“${name}” goes back to how it was on ${dateTime(b.time)}. ${data.worlds.some((x) => x.name === name) ? 'The current version is backed up first, so you can undo this.' : ''}`,
          confirm: 'Restore', icon: 'archive',
        });
        if (!ok) return;
        modal.close();
        try { await api('/api/worlds/restore', { method: 'POST', body: { name, backupId: b.id } }); } catch (e) { toast(e.message, 'bad', 9000); }
        app.poll();
      },
    }, 'Restore'))) : [h('div', { class: 'empty' }, 'No backups yet.')];
  const modal = openModal({
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
          await api('/api/worlds/backup', { method: 'POST', body: { name } });
          app.poll();
        },
      } : null,
      { label: 'Close', kind: 'primary' },
    ].filter(Boolean),
  });
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

async function localImportModal(app) {
  let cands;
  try {
    cands = (await api('/api/local-worlds')).candidates;
  } catch (e) {
    return toast(e.message, 'bad', 9000);
  }
  if (!cands.length) {
    return openModal({
      title: 'Import from this PC',
      body: [h('p', null, 'No Valheim worlds were found in this computer’s game saves.'),
        h('p', { class: 'small' }, 'This looks in the folders Valheim uses on this computer (and Steam Cloud saves). To bring a world from another PC, drop it on Add a world at the top of the Worlds page.')],
      actions: [{ label: 'Close', kind: 'primary' }],
    });
  }
  const rows = candidateRows(cands);
  openModal({
    title: 'Import from this PC',
    wide: true,
    body: [h('p', null, 'These worlds were found in Valheim’s saves on this computer. They’re copied, so the originals stay where they are.'), ...rows.el],
    actions: [{ label: 'Cancel' }, {
      label: 'Import', kind: 'primary', icon: 'check', onClick: async () => {
        await api('/api/local-worlds/import', { method: 'POST', body: { choices: rows.choices() } });
        toast('Copying the worlds…', 'info');
        app.poll();
      },
    }],
  });
}
