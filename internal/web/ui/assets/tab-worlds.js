// A game's Worlds tab (spec 6.1): its worlds with their backups, which one
// it plays, new worlds (from the game's new-world fields), adding one
// (upload, or from the owner's own game), downloads, renaming and each
// world's own settings. A game's module can add controls to each world's
// card (Valheim: difficulty and mods).

import { h, icon, api, toast, openModal, confirmDialog, fmtBytes, ago, dateTime, field } from './dom.js';
import { saveZip, downloads, today } from './download.js';
import { moduleFor, nodes } from './games.js';
import { enc, gameByID, isRunning } from './state.js';
import { schemaForm, changedValues, summary } from './forms.js';
import { startGame } from './memory.js';
import { importSection, initImport, listenForDrops, importStatus, ownsImport, recent } from './worldimport.js';

const views = new Map(); // instance -> { data, at, loading }
const menus = new Map(); // instance -> the world whose menu is open
let appRef = null;
let timer = null;
let openFor = null;

const base = (id) => `/api/games/${enc(id)}`;

function viewFor(id) {
  let v = views.get(id);
  if (!v) views.set(id, v = { data: null, at: 0, loading: false });
  return v;
}

async function load(id, force) {
  const v = viewFor(id);
  if (v.loading || (!force && Date.now() - v.at < 15000)) return;
  v.loading = true;
  try {
    v.data = await api(`${base(id)}/worlds`);
    v.at = Date.now();
  } catch (e) {
    if (!v.data) v.error = e.message;
  } finally {
    v.loading = false;
  }
  if (appRef) appRef.render();
}

const running = (g) => g.state === 'online' || g.state === 'starting';
const busy = (g) => !!(g.job && !g.job.finished);

function worldCard(app, g, d, w) {
  const selected = d.selected === w.name;
  const isRun = d.running === w.name && isRunning(g);
  const values = (d.values || {})[w.name];
  const worldFields = (d.schema && d.schema.world) || [];
  const sum = summary(worldFields, values);
  const open = menus.get(g.id) === w.name;
  const act = (action, ic, label, extra) => h('button', { role: 'menuitem', 'data-action': action, 'data-id': g.id, 'data-name': w.name, ...(extra || {}) }, icon(ic), label);
  const menu = open ? h('div', { class: 'menu-list', role: 'menu' },
    act('world-backups', 'archive', `Backups (${(w.backups || []).length})`),
    worldFields.length ? act('world-values', 'gauge', 'World settings') : null,
    act('world-rename', 'edit', 'Rename'),
    act('world-delete', 'trash', 'Delete…', { class: 'danger' })) : null;
  const r = recent.get(g.id);
  const fresh = r && r.names.includes(w.name) && Date.now() < r.until;
  return h('article', { class: `card world${selected ? ' selected' : ''}${fresh ? ' just-added' : ''}`, 'data-key': 'w:' + w.name, 'data-world': w.name },
    h('div', { class: 'row' },
      h('div', { class: 'world-name' }, w.name),
      h('span', { class: 'spacer' }),
      isRun ? h('span', { class: 'badge good' }, 'Running now') : selected ? h('span', { class: 'badge accent' }, 'Selected') : null),
    w.neverSaved ? h('div', { class: 'row' }, h('span', { class: 'badge warn' }, 'Not made yet')) : null,
    h('dl', { class: 'meta' },
      w.seed ? [h('dt', null, 'Seed'), h('dd', { class: 'mono' }, w.seed)] : null,
      h('dt', null, 'Size'), h('dd', null, fmtBytes(w.size)),
      h('dt', null, 'Last saved'), h('dd', null, w.modified ? ago(w.modified) : '–'),
      sum ? [h('dt', null, 'Settings'), h('dd', null, sum)] : null),
    nodes(moduleFor(g.game).worldCard(w, g, h)),
    h('div', { class: 'actions' },
      isRun ? null : h('button', { class: 'btn btn-primary btn-sm', 'data-action': 'world-play', 'data-id': g.id, 'data-name': w.name, disabled: busy(g) },
        icon('play'), running(g) ? 'Switch to this world' : 'Play this world'),
      downloadButton(g, w),
      h('button', { class: 'btn btn-sm', 'data-action': 'world-backup', 'data-id': g.id, 'data-name': w.name, disabled: busy(g) }, icon('archive'), 'Back up'),
      h('div', { class: 'menu' },
        h('button', { class: 'btn btn-sm btn-icon', 'data-action': 'world-menu', 'data-id': g.id, 'data-name': w.name, 'aria-label': `More for ${w.name}`, 'aria-expanded': String(open), 'aria-haspopup': 'menu' }, icon('more')),
        menu)));
}

function downloadButton(g, w) {
  const dl = downloads[`world:${g.id}:${w.name}`];
  if (!dl) {
    return h('button', { class: 'btn btn-sm', 'data-action': 'world-download', 'data-id': g.id, 'data-name': w.name, title: 'Save a copy of this world as a zip file, for example on a USB stick' },
      icon('download'), 'Download');
  }
  const pct = dl.total ? Math.min(100, Math.round((dl.sent / dl.total) * 100)) : -1;
  return h('div', { class: 'dl-progress', role: 'status' },
    h('span', { class: 'small text-2' }, dl.phase === 'packing' ? 'Packing the world…' : pct >= 0 ? `Saving… ${pct}%` : `Saving… ${fmtBytes(dl.sent)}`),
    h('div', { class: `progress${pct < 0 ? ' indeterminate' : ''}` }, h('i', { style: { width: (pct < 0 ? 35 : pct) + '%' } })));
}

function pendingCard(g, d) {
  const sum = summary((d.schema && d.schema.world) || [], (d.values || {})[d.selected]);
  return h('article', { class: 'card world selected', 'data-key': 'pending:' + d.selected },
    h('div', { class: 'row' }, h('div', { class: 'world-name' }, d.selected), h('span', { class: 'spacer' }), h('span', { class: 'badge accent' }, 'Selected')),
    h('p', { class: 'text-2' }, `This world doesn’t exist yet. ${g.short} makes it the first time it starts.`),
    sum ? h('p', { class: 'small muted' }, sum) : null);
}

function orphans(g, d) {
  if (!(d.orphans || []).length) return null;
  return [
    h('h2', { class: 'section-title' }, 'Deleted or old worlds'),
    h('p', { class: 'text-2 small', style: { marginBottom: '12px' } }, 'These only exist as backups. Restore one to bring the world back.'),
    h('div', { class: 'world-grid' }, d.orphans.map((o) => h('article', { class: 'card world', 'data-key': 'orphan:' + o.name },
      h('div', { class: 'world-name' }, o.name),
      h('p', { class: 'small muted' }, `${o.backups.length} backup${o.backups.length === 1 ? '' : 's'}${o.backups[0] ? ', newest ' + ago(o.backups[0].time) : ''}`),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'world-backups', 'data-id': g.id, 'data-name': o.name }, icon('archive'), 'Backups & restore'))))),
  ];
}

export default {
  render(app, g) {
    appRef = app;
    importStatus(g);
    const v = viewFor(g.id);
    const d = v.data;
    if (!d) return v.error ? h('div', { class: 'card' }, h('p', { class: 'muted' }, v.error)) : h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading worlds…'));
    const exists = (d.worlds || []).some((w) => w.name === d.selected);
    return h('div', null,
      importSection(app, g, d),
      h('div', { class: 'row', style: { margin: '22px 0 16px' } },
        h('p', { class: 'text-2', style: { flex: '1 1 320px' } },
          `${g.short} plays the `, h('b', null, 'selected'), ' world. Switching worlds restarts the server, and the other worlds stay safe here. ',
          h('b', null, 'Download'), ' saves a copy of a world, for example on a USB stick.'),
        h('button', { class: 'btn btn-primary', 'data-action': 'world-new', 'data-id': g.id, disabled: busy(g) }, icon('plus'), 'Create a new world')),
      h('div', { class: 'world-grid' },
        exists || !d.selected ? null : pendingCard(g, d),
        (d.worlds || []).map((w) => worldCard(app, g, d, w))),
      !(d.worlds || []).length && !d.selected ? h('div', { class: 'empty' }, 'No worlds yet. Create one, or add one you already have.') : null,
      orphans(g, d),
      d.folder ? h('p', { class: 'small muted', style: { marginTop: '18px' } }, 'Worlds are kept in ', h('span', { class: 'mono' }, d.folder)) : null);
  },
  mount(app, g) {
    appRef = app;
    openFor = g.id;
    initImport(app);
    registerActions(app);
    listenForDrops(g.id, true);
    load(g.id, true);
    clearInterval(timer);
    timer = setInterval(() => { if (openFor) load(openFor, false); }, 15000);
    document.addEventListener('click', closeMenus);
  },
  unmount(app, g) {
    clearInterval(timer);
    if (g) listenForDrops(g.id, false);
    if (g) menus.delete(g.id);
    openFor = null;
    document.removeEventListener('click', closeMenus);
  },
  onJobDone(app, g) { load(g.id, true); },
  ownsJob: ownsImport,
  afterRender(app, g) {
    // Show a world that was just added.
    const r = recent.get(g.id);
    if (!r || r.scrolled || Date.now() > r.until || !r.names.length) return;
    const card = [...document.querySelectorAll('[data-world]')].find((el) => el.dataset.world === r.names[0]);
    if (card) {
      r.scrolled = true;
      card.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
  },
};

function closeMenus(e) {
  if (openFor && menus.get(openFor) && !e.target.closest('.menu')) {
    menus.delete(openFor);
    appRef.render();
  }
}

function findWorld(d, name) {
  return (d.worlds || []).find((w) => w.name === name) || (d.orphans || []).find((o) => o.name === name);
}

function registerActions(app) {
  const A = app.actions;
  const ctx = (dset) => {
    const g = gameByID(app.status, dset.id);
    const d = g && viewFor(g.id).data;
    return g && d ? { g, d } : null;
  };
  A['world-menu'] = (dset) => {
    menus.set(dset.id, menus.get(dset.id) === dset.name ? null : dset.name);
    app.render();
  };
  A['world-play'] = async (dset) => {
    const c = ctx(dset);
    if (!c) return;
    const { g } = c;
    const on = running(g);
    if (on) {
      const n = Math.max(0, g.count);
      const ok = await confirmDialog({
        title: `Switch to “${dset.name}”?`,
        message: `${g.short} saves the current world and restarts with “${dset.name}”. ${n ? `The ${n} player${n === 1 ? '' : 's'} online will be disconnected for a minute or two.` : 'It takes a minute or two.'}`,
        confirm: 'Switch world', icon: 'refresh',
      });
      if (!ok) return;
    }
    try {
      await api(`${base(g.id)}/worlds/select`, { method: 'POST', body: { name: dset.name, restart: on } });
      if (on) toast(`Switching to “${dset.name}”…`, 'good');
    } catch (e) {
      toast(e.message, 'bad', 9000);
      return;
    }
    load(g.id, true);
    if (!on) await startGame(app, g);
    else app.poll();
  };
  A['world-download'] = (dset) => {
    const c = ctx(dset);
    if (!c) return;
    const note = c.d.running === dset.name && isRunning(c.g) ? 'It has the world as of its last save.' : '';
    saveZip(`world:${c.g.id}:${dset.name}`, `${base(c.g.id)}/worlds/download?name=${encodeURIComponent(dset.name)}`, `${c.g.short} - ${dset.name} ${today()}.zip`, note, () => app.render());
  };
  A['world-backup'] = async (dset) => {
    try { await api(`${base(dset.id)}/worlds/backup`, { method: 'POST', body: { name: dset.name } }); } catch (e) { toast(e.message, 'bad', 9000); }
    app.poll();
  };
  A['world-delete'] = async (dset) => {
    const c = ctx(dset);
    menus.delete(dset.id);
    if (!c) return;
    if (dset.name === c.d.selected) {
      toast('This is the selected world. Switch to another world before deleting it.', 'warn', 8000);
      return app.render();
    }
    const ok = await confirmDialog({
      title: `Delete “${dset.name}”?`,
      message: 'The world is removed. A backup copy is kept, so it can be brought back later from “Deleted or old worlds”.',
      confirm: 'Delete world', kind: 'danger', icon: 'trash',
    });
    if (!ok) return app.render();
    try { await api(`${base(dset.id)}/worlds/delete`, { method: 'POST', body: { name: dset.name } }); } catch (e) { toast(e.message, 'bad', 9000); }
    load(dset.id, true);
    app.poll();
  };
  A['world-rename'] = (dset) => {
    menus.delete(dset.id);
    app.render();
    const input = h('input', { class: 'input', value: dset.name, maxlength: 64 });
    openModal({
      title: `Rename “${dset.name}”`,
      body: [field('New name', input, 'Letters, numbers and spaces work best.')],
      actions: [{ label: 'Cancel' }, {
        label: 'Rename', kind: 'primary', onClick: async () => {
          await api(`${base(dset.id)}/worlds/rename`, { method: 'POST', body: { name: dset.name, newName: input.value.trim() } });
          toast('World renamed.', 'good');
          load(dset.id, true);
        },
      }],
    });
  };
  A['world-values'] = (dset) => {
    const c = ctx(dset);
    menus.delete(dset.id);
    app.render();
    if (!c) return;
    const fields = (c.d.schema && c.d.schema.world) || [];
    const saved = (c.d.values || {})[dset.name] || {};
    const state = { values: { ...saved }, errors: {} };
    const body = h('div', { class: 'form' });
    const draw = () => body.replaceChildren(...schemaForm(fields, state, { id: 'wv', prefix: 'world.', running: running(c.g) }).filter(Boolean));
    draw();
    openModal({
      title: `Settings for “${dset.name}”`,
      wide: true,
      body: [body, h('p', { class: 'small' }, running(c.g) && c.d.running === dset.name ? 'Used the next time the server starts this world.' : 'Used when the server plays this world.')],
      actions: [{ label: 'Cancel' }, {
        label: 'Save', kind: 'primary', onClick: async () => {
          const values = changedValues(fields, saved, state.values);
          if (!Object.keys(values).length) return true;
          try {
            await api(`${base(dset.id)}/worlds/values`, { method: 'POST', body: { name: dset.name, values } });
          } catch (e) {
            state.errors = (e.data && e.data.fields) || {};
            draw();
            throw e;
          }
          toast('World settings saved.', 'good');
          load(dset.id, true);
          app.poll();
          return true;
        },
      }],
    });
  };
  A['world-backups'] = (dset) => {
    menus.delete(dset.id);
    app.render();
    const c = ctx(dset);
    if (c) backupsModal(app, c.g, c.d, dset.name);
  };
  A['world-new'] = (dset) => {
    const c = ctx(dset);
    if (c) newWorldModal(app, c.g, c.d);
  };
}

function newWorldModal(app, g, d) {
  const name = h('input', { class: 'input', placeholder: 'For example Second Voyage', maxlength: 64 });
  const fields = (d.schema && d.schema.newWorld) || [];
  const state = { values: {}, errors: {} };
  const form = h('div', { class: 'form' });
  const draw = () => form.replaceChildren(...schemaForm(fields, state, { id: 'nw', prefix: 'newWorld.' }).filter(Boolean));
  draw();
  const on = running(g);
  const now = h('input', { type: 'checkbox', checked: !on });
  openModal({
    title: `A new ${g.short} world`,
    wide: true,
    body: [
      field('World name', name),
      form,
      h('label', { class: 'row', style: { gap: '8px', cursor: 'pointer', color: 'var(--text)' } }, now,
        on ? 'Restart the server and switch to it now' : 'Start the server with it now'),
      h('p', { class: 'small text-2' }, `${g.short} makes the world the first time it starts with it.`),
    ],
    actions: [{ label: 'Cancel' }, {
      label: 'Create world', kind: 'primary', icon: 'plus', onClick: async () => {
        const values = {};
        for (const f of fields) values[f.key] = state.values[f.key] != null ? String(state.values[f.key]) : f.default || '';
        try {
          await api(`/api/games/${enc(g.id)}/worlds/create`, { method: 'POST', body: { name: name.value.trim(), values, restart: on && now.checked } });
        } catch (e) {
          state.errors = (e.data && e.data.fields) || {};
          draw();
          throw e;
        }
        toast(`“${name.value.trim()}” is now the selected world.`, 'good');
        load(g.id, true);
        if (!on && now.checked) await startGame(app, gameByID(app.status, g.id) || g);
        else app.poll();
      },
    }],
  });
}

function backupsModal(app, g, d, name) {
  const w = findWorld(d, name);
  if (!w) return;
  const runningNow = d.running === name && isRunning(g);
  const exists = (d.worlds || []).some((x) => x.name === name);
  let modal = null;
  const rows = (w.backups || []).length ? w.backups.map((b) => h('div', { class: 'list-row' },
    h('div', { class: 'grow' },
      h('div', { class: 'name', style: { whiteSpace: 'normal' } }, b.label || 'Backup'),
      h('div', { class: 'sub' }, `${dateTime(b.time)} · ${fmtBytes(b.size)}`)),
    h('button', {
      class: 'btn btn-sm btn-icon', 'aria-label': 'Download this backup', title: 'Download',
      onclick: () => saveZip(`backup:${g.id}:${b.id}`, `/api/games/${enc(g.id)}/worlds/backup-download?name=${encodeURIComponent(name)}&id=${encodeURIComponent(b.id)}`,
        `${g.short} - ${name} backup ${today(b.time)}.zip`, '', () => {}),
    }, icon('download')),
    h('button', {
      class: 'btn btn-sm', disabled: runningNow, title: runningNow ? 'Stop the server first' : null,
      onclick: async () => {
        const ok = await confirmDialog({
          title: 'Restore this backup?',
          message: `“${name}” goes back to how it was on ${dateTime(b.time)}. ${exists ? 'The current version is backed up first, so this can be undone.' : ''}`,
          confirm: 'Restore', icon: 'archive',
        });
        if (!ok) return;
        modal.close();
        try { await api(`/api/games/${enc(g.id)}/worlds/restore`, { method: 'POST', body: { name, backupId: b.id } }); } catch (e) { toast(e.message, 'bad', 9000); }
        app.poll();
      },
    }, 'Restore'))) : [h('div', { class: 'empty' }, 'No backups yet.')];
  modal = openModal({
    title: `Backups of “${name}”`,
    wide: true,
    body: [
      runningNow ? h('div', { class: 'callout warn' }, icon('info'), h('div', { class: 'body small' }, 'This world is running. Stop the server before restoring a backup.')) : null,
      h('p', { class: 'small' }, 'The app keeps a copy before anything is replaced or deleted, and backs worlds up while the server runs.'),
      h('div', { class: 'list' }, rows),
    ],
    actions: [
      exists ? { label: 'Back up now', icon: 'archive', onClick: async () => { await api(`/api/games/${enc(g.id)}/worlds/backup`, { method: 'POST', body: { name } }); app.poll(); } } : null,
      { label: 'Close', kind: 'primary' },
    ].filter(Boolean),
  });
}
