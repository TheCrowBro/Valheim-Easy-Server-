// A game's Settings tab (spec 6.1): its server settings (drawn from its
// schema, the rarely needed ones under "More settings"), its ports, when it
// stops and starts by itself, its Discord posts, turning it off, and
// removing it. Only what was changed is sent, and the app merges it into
// what it has (spec 6.2), so a page that is out of date never blanks a
// setting changed elsewhere.

import { h, icon, api, toast, confirmDialog, toggle } from './dom.js';
import { enc, gameByID, isRunning, busyJob, legacyID, routerOpen } from './state.js';
import { schemaForm, changedValues } from './forms.js';
import { loadCatalog, portLabel } from './catalog.js';
import { callout } from './parts.js';

const views = new Map(); // instance -> its form
let appRef = null;
let openFor = null;

function viewFor(id) {
  let v = views.get(id);
  if (!v) views.set(id, v = { view: null, values: {}, ports: {}, autoStop: null, startWithApp: null, discord: {}, errors: {}, saving: false, testing: false });
  return v;
}

const base = (id) => `/api/games/${enc(id)}`;

async function load(id, keep) {
  const v = viewFor(id);
  try {
    const view = await api(`${base(id)}/settings`);
    const dirtyBefore = keep && dirty(v);
    v.view = view;
    if (!dirtyBefore) reset(v);
  } catch (e) {
    v.error = e.message;
  }
  if (appRef) appRef.render();
}

function reset(v) {
  v.values = { ...(v.view.values.server || {}) };
  v.ports = {};
  for (const p of v.view.ports || []) v.ports[p.role] = String(p.port);
  v.autoStop = String(v.view.autoStopMinutes ?? 60);
  v.startWithApp = !!v.view.startWithApp;
  const d = v.view.discord || {};
  v.discord = { on: !!d.on, own: !!d.webhook, webhook: d.webhook || '', stopped: !!d.stopped, daily: !!d.daily, password: !!d.password };
  v.errors = {};
}

const serverFields = (v) => (v.view.schema || []).filter((f) => f.scope === 'server');

// change is what the form changed, in the shape the app takes.
function change(v) {
  const out = {};
  const values = changedValues(serverFields(v), v.view.values.server || {}, v.values);
  if (Object.keys(values).length) out.values = values;
  const ports = {};
  for (const p of v.view.ports || []) {
    const n = Number(v.ports[p.role]);
    if (v.ports[p.role] !== '' && n !== p.port) ports[p.role] = n;
  }
  if (Object.keys(ports).length) out.ports = ports;
  const mins = Number(v.autoStop);
  if (v.autoStop !== '' && mins !== (v.view.autoStopMinutes ?? 60)) out.autoStopMinutes = mins;
  if (v.startWithApp !== !!v.view.startWithApp) out.startWithApp = v.startWithApp;
  const was = v.view.discord || {};
  const d = {};
  if (v.discord.on !== !!was.on) d.on = v.discord.on;
  const hook = v.discord.own ? v.discord.webhook.trim() : '';
  if (hook !== (was.webhook || '')) d.webhook = hook;
  for (const k of ['stopped', 'daily', 'password']) if (v.discord[k] !== !!was[k]) d[k] = v.discord[k];
  if (Object.keys(d).length) out.discord = d;
  return out;
}

const dirty = (v) => !!(v.view && Object.keys(change(v)).length);

function section(title, ...body) {
  return h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, title)), ...body);
}

function switchRow(title, help, attrs) {
  return h('div', { class: 'toggle-row' },
    h('div', { class: 'grow' }, h('div', { class: 'title' }, title), help ? h('div', { class: 'help' }, help) : null),
    toggle({ 'aria-label': title, ...attrs }));
}

function portsSection(g, v) {
  const ports = v.view.ports || [];
  if (!ports.length) {
    return section('Ports', h('p', { class: 'text-2' }, `${g.short} doesn’t need any ports opened: friends join through the game’s own service.`));
  }
  const routerOf = (role) => ((g.ports || []).find((p) => p.role === role) || {}).router || '';
  return section('Ports',
    h('p', { class: 'text-2 small', style: { marginBottom: '12px' } },
      'The app picks free ports, so games never get in each other’s way. A port you type yourself is kept: the app never moves it.'),
    h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Port'), h('th', null, 'Protocol'), h('th', null, 'Number'), h('th', null, ''), h('th', null, 'Friends outside'))),
      h('tbody', null, ports.map((p) => {
        const id = `port-${g.id}-${p.role}`.replace(/[^\w-]/g, '_');
        const err = v.errors['ports.' + p.role] || v.errors[p.role];
        const r = routerOf(p.role);
        return h('tr', { 'data-key': 'port:' + p.role },
          h('td', null, h('label', { for: id }, portLabel(g.game, p.role))),
          h('td', null, (p.protos || []).join(' + ').toUpperCase()),
          h('td', null, h('input', { class: `input${err ? ' invalid' : ''}`, id, type: 'number', min: 1024, max: 65535, value: v.ports[p.role] ?? '', style: { width: '110px', minHeight: '36px' },
            oninput: (e) => { v.ports[p.role] = e.target.value; appRef.render(); } }),
          err ? h('div', { class: 'error small' }, err) : null),
          h('td', null, h('span', { class: `badge${p.chosen ? ' accent' : ''}` }, p.chosen ? 'Chosen by you' : 'Picked by the app')),
          h('td', null, !p.public ? h('span', { class: 'small muted' }, 'This computer only')
            : routerOpen(r) ? h('span', { class: 'badge good' }, r === 'open' ? 'Open on the router' : 'Forwarded by hand')
              : r === 'failed' ? h('span', { class: 'badge warn' }, 'Router said no')
                : h('span', { class: 'small muted' }, r === 'off' ? 'Router ports off' : r === 'mapping' ? 'Opening…' : 'Not checked yet')));
      })))),
    h('div', { class: 'row', style: { marginTop: '12px' } },
      h('button', { class: 'btn btn-sm', 'data-action': 'ports-pick', 'data-id': g.id, disabled: isRunning(g) }, icon('refresh'), 'Pick free ports'),
      isRunning(g) ? h('span', { class: 'small muted' }, 'Stop the server to change its ports.') : h('span', { class: 'small muted' }, 'Picks new ports for the ones you didn’t choose.')));
}

function autoSection(g, v) {
  const err = v.errors.autoStopMinutes;
  return section('Stopping and starting by itself',
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Stop when nobody has played for a while'),
        h('div', { class: 'row', style: { marginTop: '8px' } }, 'After',
          h('input', { class: `input${err ? ' invalid' : ''}`, type: 'number', min: 0, max: 1440, value: v.autoStop, style: { width: '96px' }, 'aria-label': 'Minutes',
            oninput: (e) => { v.autoStop = e.target.value; delete v.errors.autoStopMinutes; appRef.render(); } }),
          'minutes with nobody on'),
        h('div', { class: 'help' }, '0 means never. Stopping an empty game frees its memory, and lets the computer sleep.'),
        err ? h('div', { class: 'error' }, err) : null)),
    switchRow('Start it when the app starts', 'For example after the computer restarts.', {
      checked: v.startWithApp, onchange: (e) => { v.startWithApp = e.target.checked; appRef.render(); },
    }));
}

function discordSection(g, v) {
  const d = v.discord;
  const was = v.view.discord || {};
  const err = v.errors['discord.webhook'] || v.errors.webhook;
  const set = (k) => (e) => { d[k] = e.target.checked; appRef.render(); };
  return section('Discord',
    switchRow(`Post in Discord when ${g.short} is online`, 'With how to join, so friends don’t have to ask.', { checked: d.on, onchange: set('on') }),
    d.on ? h('div', { class: 'form discord-form' },
      h('div', { class: 'choices' },
        h('label', { class: `choice${!d.own ? ' on' : ''}` },
          h('input', { type: 'radio', name: `dc-${g.id}`, checked: !d.own, onchange: () => { d.own = false; appRef.render(); } }),
          h('div', null, h('div', { class: 'title' }, 'The main channel'), h('div', { class: 'help' }, 'The one set on the Discord page, shared by every game without its own.'))),
        h('label', { class: `choice${d.own ? ' on' : ''}` },
          h('input', { type: 'radio', name: `dc-${g.id}`, checked: d.own, onchange: () => { d.own = true; appRef.render(); } }),
          h('div', null, h('div', { class: 'title' }, `A channel just for ${g.short}`), h('div', { class: 'help' }, 'Its Discord command then works only in that channel.')))),
      d.own ? h('div', { class: 'field' }, h('label', { for: `dc-hook-${g.id}` }, 'The channel’s webhook link'),
        h('div', { class: 'input-group' },
          h('input', { class: `input${err ? ' invalid' : ''}`, id: `dc-hook-${g.id}`, value: d.webhook, placeholder: 'https://discord.com/api/webhooks/…', spellcheck: 'false', autocomplete: 'off',
            oninput: (e) => { d.webhook = e.target.value; delete v.errors['discord.webhook']; appRef.render(); } }),
          h('button', { class: 'btn', type: 'button', 'data-action': 'game-discord-test', 'data-id': g.id, disabled: v.testing || !d.webhook.trim() }, icon('send'), v.testing ? 'Sending…' : 'Send a test')),
        h('div', { class: 'help' }, 'Discord page → How to get a webhook link explains where to find it.'),
        err ? h('div', { class: 'error' }, err) : null) : null,
      was.on && was.webhook && !was.guildOK ? callout('warn', 'alert', h('p', { class: 'small' },
        'This channel is in another Discord server than the main channel, so the bot’s command for this game won’t work there.')) : null,
      switchRow('Also post when it stops', 'So nobody tries to join a server that’s gone.', { checked: d.stopped, onchange: set('stopped') }),
      switchRow('Post who died the most each day', 'At 23:59, if anyone played that day.', { checked: d.daily, onchange: set('daily') }),
      switchRow('Include the password', 'Hidden as a spoiler until it’s clicked. Only if the channel is just for your friends.', { checked: d.password, onchange: set('password') })) : null);
}

function maintenanceSection(app, g) {
  const on = isRunning(g);
  const busy = !!busyJob(g);
  const legacy = legacyID(app.status) === g.id;
  return section('Maintenance',
    h('div', { class: 'stack', style: { gap: '14px' } },
      h('div', { class: 'row' },
        h('button', { class: 'btn', 'data-action': 'game-install', 'data-id': g.id, disabled: on || busy || g.canRun === 'no' }, icon('refresh'), 'Update the server now'),
        h('button', { class: 'btn', 'data-action': 'game-repair', 'data-id': g.id, disabled: on || busy || !g.installed }, icon('wrench'), 'Repair server files'),
        on ? h('span', { class: 'small muted' }, 'Stop the server to update or repair it.') : null),
      h('div', { class: 'toggle-row', style: { borderTop: '1px solid var(--line)', paddingTop: '14px' } },
        h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Turn this game off'),
          h('div', { class: 'help' }, 'While it’s off it can’t be started, from here, Discord or anything else, until it’s turned on again. Its worlds and settings stay.')),
        toggle({ checked: !!g.disabled, 'aria-label': 'Turn this game off', 'data-action': 'game-disable', 'data-id': g.id })),
      h('div', { class: 'row', style: { paddingTop: '14px', borderTop: '1px solid var(--line)' } },
        h('div', { class: 'grow', style: { flex: '1 1 300px' } }, h('div', { class: 'title', style: { fontWeight: 600 } }, 'Remove this game'),
          h('div', { class: 'small muted' }, legacy
            ? 'It’s stopped and taken out of the panel, Discord and everything that starts games. Its files, worlds and settings stay exactly where they are, so adding it again brings it back.'
            : 'It’s stopped and its server is deleted. Its worlds are kept as backups, with its players and records, so adding it again brings them back.')),
        h('button', { class: 'btn btn-danger', 'data-action': 'game-remove', 'data-id': g.id, disabled: busy }, icon('trash'), 'Remove…'))));
}

export default {
  render(app, g) {
    appRef = app;
    const v = viewFor(g.id);
    if (app.fieldErrors[g.id]) {
      Object.assign(v.errors, app.fieldErrors[g.id]);
      delete app.fieldErrors[g.id];
    }
    if (!v.view) return h('div', { class: 'card' }, h('p', { class: 'muted' }, v.error || 'Loading settings…'));
    const fields = serverFields(v);
    const changed = dirty(v);
    const other = Object.entries(v.errors).filter(([k]) => !k.startsWith('server.') && !k.startsWith('ports.') && !k.startsWith('discord.') && k !== 'autoStopMinutes');
    return h('div', { class: 'stack' },
      other.length ? callout('bad', 'alert', other.map(([, msg]) => h('p', null, msg))) : null,
      section('Server', fields.length
        ? h('div', { class: 'form' }, schemaForm(fields, v, { id: 'set-' + g.id, prefix: 'server.', running: isRunning(g), onChange: () => app.render() }))
        : h('p', { class: 'text-2' }, 'This game has no settings of its own.')),
      portsSection(g, v),
      autoSection(g, v),
      discordSection(g, v),
      maintenanceSection(app, g),
      changed ? h('div', { class: 'savebar', role: 'region', 'aria-label': 'Unsaved changes' },
        h('span', null, 'You have unsaved changes.'),
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn btn-ghost', 'data-action': 'game-settings-discard', 'data-id': g.id }, 'Discard'),
        h('button', { class: 'btn btn-primary', 'data-action': 'game-settings-save', 'data-id': g.id, disabled: v.saving }, icon('save'), v.saving ? 'Saving…' : 'Save changes')) : null);
  },
  mount(app, g) {
    appRef = app;
    openFor = g.id;
    loadCatalog().then(() => app.render()).catch(() => {});
    load(g.id, true);
    const A = app.actions;
    A['game-settings-discard'] = (d) => { const v = viewFor(d.id); if (v.view) reset(v); app.render(); };
    A['game-settings-save'] = async (d) => {
      const v = viewFor(d.id);
      const g2 = gameByID(app.status, d.id);
      const body = change(v);
      if (!Object.keys(body).length) return;
      v.saving = true;
      app.render();
      try {
        await api(`${base(d.id)}/settings`, { method: 'POST', body });
        v.errors = {};
        toast(g2 && isRunning(g2) && body.values ? 'Saved. Some settings are used after the next restart.' : 'Settings saved.', 'good');
        await load(d.id, false);
      } catch (e) {
        v.errors = (e.data && e.data.fields) || {};
        toast(e.message, 'bad', 9000);
      } finally {
        v.saving = false;
        app.render();
        app.poll();
      }
    };
    A['ports-pick'] = async (d) => {
      try {
        await api(`${base(d.id)}/ports/pick`, { method: 'POST', body: {} });
        toast('New ports picked.', 'good');
      } catch (e) {
        toast(e.message, 'bad', 9000);
      }
      const v = viewFor(d.id);
      if (v.view) v.ports = {};
      await load(d.id, false);
      app.poll();
    };
    A['game-discord-test'] = async (d) => {
      const v = viewFor(d.id);
      v.testing = true;
      app.render();
      try {
        await api('/api/discord/test', { method: 'POST', body: { webhook: v.discord.webhook.trim(), game: d.id } });
        delete v.errors['discord.webhook'];
        toast('Test message sent. Look in the Discord channel.', 'good');
      } catch (e) {
        const f = (e.data && e.data.fields) || {};
        if (f.discordWebhook || f.webhook) v.errors['discord.webhook'] = f.discordWebhook || f.webhook;
        toast(f.discordWebhook || f.webhook || e.message, 'bad', 10000);
      } finally {
        v.testing = false;
        app.render();
      }
    };
    A['game-disable'] = async (d, el) => {
      const g2 = gameByID(app.status, d.id);
      const off = el.checked;
      if (off && g2 && isRunning(g2) && !await confirmDialog({ title: `Turn ${g2.short} off?`, message: 'It’s running, so it’s saved and stopped first.', confirm: 'Stop and turn off', kind: 'danger' })) {
        el.checked = false;
        return;
      }
      try {
        await api(`${base(d.id)}/settings`, { method: 'POST', body: { disabled: off } });
        toast(off ? 'Turned off. Turn it on here when you want it again.' : 'Turned on again.', 'good');
      } catch (e) {
        el.checked = !off;
        toast(e.message, 'bad', 9000);
      }
      app.poll();
    };
    A['game-remove'] = async (d) => {
      const g2 = gameByID(app.status, d.id);
      if (!g2) return;
      const legacy = legacyID(app.status) === g2.id;
      const ok = await confirmDialog({
        title: `Remove ${g2.short}?`,
        message: legacy
          ? `${g2.short} is stopped and taken out of the panel. Nothing of it is deleted: its files, worlds and settings stay where they are. Add it again from Add a game to bring it back.`
          : `${g2.short} is stopped, each world is backed up, and its server files are deleted. Its worlds, players and records are kept, and come back if you add it again.`,
        confirm: 'Remove', kind: 'danger', icon: 'trash',
      });
      if (!ok) return;
      try {
        await api(`${base(d.id)}/remove`, { method: 'POST', body: {} });
        toast(`Removing ${g2.short}…`, 'info');
        loadCatalog(true).catch(() => {});
        app.go('overview');
      } catch (e) {
        toast(e.message, 'bad', 9000);
      }
      app.poll();
    };
    window.addEventListener('beforeunload', onLeave);
  },
  unmount() {
    openFor = null;
    window.removeEventListener('beforeunload', onLeave);
  },
};

function onLeave(e) {
  const v = openFor && views.get(openFor);
  if (v && dirty(v)) { e.preventDefault(); e.returnValue = ''; }
}
