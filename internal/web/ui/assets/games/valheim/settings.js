// Valheim's Settings tab: the server's own settings as the old panel's
// Settings page had them (its Server, Discord, Automatic, Saving & backups,
// Folders & advanced and Maintenance sections, with the same words and the
// password maker), plus what every game's Settings tab has in the new
// panel (spec 6.1): auto-stop, start with the app, this game's own Discord
// channel, turning the game off, and removing it.
//
// The settings app-wide on the old page (resting and waking, starting from
// Discord through the JetKVM, app updates, starting the app with the
// computer, the firewall and closing the app) live on the shell's App
// pages now.
//
// It reads GET /api/games/{id}/settings and saves only what changed with
// POST /api/games/{id}/settings, which merges it on the server, so a page
// that is out of date never blanks a field (spec 6.2). Every value is
// text there ("2456", "true"), as the game's settings are (spec 2.2).

import { h, icon, api, toast, confirmDialog, toggle } from '../../dom.js';
import { gameApi, perGame, busy, isLegacy, redrawer } from './common.js';

// The settings that are numbers, so a box left empty means 0 as before.
const NUMBERS = ['port', 'saveIntervalMinutes', 'backups', 'backupShortMinutes', 'backupLongMinutes'];

// What the tab loaded and what is being edited, per instance.
const S = perGame(() => ({ view: null, draft: null, errors: {}, saving: false, testing: false, paths: null }));
const view = redrawer();
let shown = null; // the instance whose Settings tab is open

// base is what the app has now, in the form the tab edits. discord.own
// is the tab's own: whether the game posts in its own channel (a webhook
// of its own) rather than the main one (no webhook, "").
function base(v) {
  const d = v.discord || {};
  return {
    values: { ...(v.values && v.values.server) },
    autoStopMinutes: v.autoStopMinutes,
    startWithApp: !!v.startWithApp,
    discord: { on: !!d.on, own: !!d.webhook, webhook: d.webhook || '', stopped: !!d.stopped, daily: !!d.daily, password: !!d.password },
  };
}

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const dirty = (s) => !!(s.view && s.draft && !same(s.draft, base(s.view)));

// load reads the settings. When the tab is opened again with changes that
// weren't saved, they're kept (on top of the settings as they are now), so
// visiting another tab doesn't lose them.
async function load(game, keepChanges) {
  const s = S(game);
  const was = keepChanges && dirty(s) ? { base: base(s.view), draft: s.draft } : null;
  try {
    s.view = await api(gameApi(game, '/settings'));
    s.draft = structuredClone(base(s.view));
    if (was) {
      for (const k of Object.keys(was.draft.values)) {
        if (was.draft.values[k] !== was.base.values[k]) s.draft.values[k] = was.draft.values[k];
      }
      for (const k of ['autoStopMinutes', 'startWithApp']) if (!same(was.draft[k], was.base[k])) s.draft[k] = was.draft[k];
      for (const k of Object.keys(was.draft.discord)) {
        if (!same(was.draft.discord[k], was.base.discord[k])) s.draft.discord[k] = was.draft.discord[k];
      }
    } else {
      s.errors = {};
    }
  } catch (e) {
    toast(e.message, 'bad');
  }
  view.render();
}

// The default folders, shown in the folder boxes while they're empty. The
// game's settings answer doesn't carry them, so they come from the legacy
// game's own settings route, as the old page had them.
async function loadPaths(game) {
  if (!isLegacy(game)) return;
  try {
    S(game).paths = (await api('/api/settings')).paths || null;
    view.render();
  } catch { /* the boxes just stay without the hint */ }
}

// change is what to send: only what differs from what the app has.
function change(s) {
  const was = base(s.view);
  const out = {};
  const values = {};
  for (const [k, v] of Object.entries(s.draft.values)) if (v !== was.values[k]) values[k] = v;
  if (Object.keys(values).length) out.values = values;
  if (s.draft.autoStopMinutes !== was.autoStopMinutes) out.autoStopMinutes = s.draft.autoStopMinutes;
  if (s.draft.startWithApp !== was.startWithApp) out.startWithApp = s.draft.startWithApp;
  const discord = {};
  for (const k of ['on', 'stopped', 'daily', 'password']) {
    if (s.draft.discord[k] !== was.discord[k]) discord[k] = s.draft.discord[k];
  }
  const link = (d) => (d.own ? d.webhook.trim() : '');
  if (link(s.draft.discord) !== link(was.discord)) discord.webhook = link(s.draft.discord);
  if (Object.keys(discord).length) out.discord = discord;
  return out;
}

// errorsFrom reads the app's field errors: the server's settings come back
// as "server.<key>", the rest as they are named in the change.
function errorsFrom(fields) {
  const out = {};
  for (const [k, v] of Object.entries(fields || {})) out[k.replace(/^server\./, '')] = v;
  return out;
}

// ---------- the form's parts ----------

function setValue(game, name, value) {
  const s = S(game);
  s.draft.values[name] = value;
  delete s.errors[name];
  view.render();
}

function input(game, name, attrs = {}) {
  const s = S(game);
  return h('input', {
    class: `input${s.errors[name] ? ' invalid' : ''}`, id: `vh-f-${name}`, value: s.draft.values[name] ?? '',
    oninput: (e) => setValue(game, name, NUMBERS.includes(name) && e.target.value === '' ? '0' : e.target.value),
    ...attrs,
  });
}

function fieldFor(game, name, label, el, help) {
  const err = S(game).errors[name];
  return h('div', { class: 'field' }, h('label', { for: `vh-f-${name}` }, label), el, help ? h('div', { class: 'help' }, help) : null,
    err ? h('div', { class: 'error', role: 'alert' }, err) : null);
}

function switchRow(title, help, checked, onchange, opts = {}) {
  return h('div', { class: 'toggle-row' },
    h('div', { class: 'grow' }, h('div', { class: 'title' }, title), help ? h('div', { class: 'help' }, help) : null, opts.extra || null,
      opts.error ? h('div', { class: 'error' }, opts.error) : null),
    toggle({ checked: !!checked, 'aria-label': title, disabled: opts.disabled, onchange: (e) => onchange(e.target.checked) }));
}

const valueSwitch = (game, name, title, help) => {
  const s = S(game);
  return switchRow(title, help, s.draft.values[name] === 'true', (on) => setValue(game, name, String(on)), { error: s.errors[name] });
};

function serverSection(game) {
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Server')),
    h('div', { class: 'form' },
      h('div', { class: 'field-row' },
        fieldFor(game, 'serverName', 'Server name', input(game, 'serverName', { maxlength: 64 }), 'Shown in Valheim’s server list.'),
        fieldFor(game, 'password', 'Server password',
          h('div', { class: 'input-group' },
            input(game, 'password', { type: 'text', maxlength: 64, autocomplete: 'off', spellcheck: 'false' }),
            h('button', { class: 'btn', type: 'button', title: 'Make up a new password', onclick: () => newPassword(game) }, icon('sparkle'), 'New')),
          'Friends type this when they join. At least 5 characters. It can’t be part of the server or world name.')),
      valueSwitch(game, 'crossplay', 'Crossplay (recommended)',
        'Lets friends on Xbox, PlayStation, Switch 2 and Game Pass join, and gives you a join code. Joining by code works without setting up your router.'),
      valueSwitch(game, 'public', 'Show in the public server list',
        'Anyone can find the server by name in Valheim (they still need the password). Leave off to keep it private.'),
      h('details', null, h('summary', { style: { cursor: 'pointer' } }, 'Network port'),
        h('div', { style: { marginTop: '12px', maxWidth: '260px' } },
          fieldFor(game, 'port', 'Game port', input(game, 'port', { type: 'number', min: 1024, max: 65534 }),
            'Valheim uses this port and the next one (UDP). 2456 is normal; only change it if something else uses it.')))));
}

function setDiscord(game, key, value) {
  const s = S(game);
  s.draft.discord[key] = value;
  delete s.errors['discord.' + key];
  if (key === 'webhook') delete s.errors.discordWebhook;
  view.render();
}

function discordSection(game) {
  const s = S(game);
  const d = s.draft.discord;
  const own = d.own;
  const link = d.webhook.trim();
  const err = s.errors['discord.webhook'] || s.errors.discordWebhook;
  const option = (value, title, help) => h('label', { class: `choice${own === value ? ' on' : ''}` },
    h('input', { type: 'radio', name: `vh-channel-${game.id}`, checked: own === value, onchange: () => setDiscord(game, 'own', value) }),
    h('div', null, h('div', { class: 'title' }, title), h('div', { class: 'help' }, help)));
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Discord')),
    switchRow('Post the join code in Discord',
      'Each time the server is online, its join code is posted in a Discord channel, so friends can join without asking for it.',
      d.on, (on) => setDiscord(game, 'on', on)),
    d.on ? h('div', { class: 'form discord-form' },
      h('div', { class: 'choices' },
        option(false, 'The main channel', ['The one every game posts in unless it has its own. It’s set on the ', h('a', { href: '#/app/discord' }, 'Discord'), ' page.']),
        option(true, 'Its own channel', 'Only Valheim’s posts go there, and /valheim only works in that channel.')),
      own ? fieldFor(game, 'discord.webhook', 'The channel’s webhook link',
        h('div', { class: 'input-group' },
          h('input', {
            class: `input${err ? ' invalid' : ''}`, id: 'vh-f-discord.webhook', value: d.webhook, placeholder: 'https://discord.com/api/webhooks/…',
            spellcheck: 'false', autocomplete: 'off', oninput: (e) => setDiscord(game, 'webhook', e.target.value),
          }),
          h('button', { class: 'btn', type: 'button', disabled: s.testing || !link, onclick: () => testDiscord(game) },
            icon('send'), s.testing ? 'Sending…' : 'Send a test'))) : null,
      own && !s.view.discord.guildOK && link && link === (s.view.discord.webhook || '') ? h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body small' },
        h('p', null, 'This channel is in a different Discord server from the main channel, so /valheim doesn’t work in it.'))) : null,
      own ? h('details', { class: 'where', open: !link },
        h('summary', null, 'How to get the webhook link'),
        h('ol', { class: 'steps' },
          h('li', null, 'In Discord, click the gear next to the channel’s name (', h('b', null, 'Edit Channel'), ').'),
          h('li', null, 'Open ', h('b', null, 'Integrations'), ' and click ', h('b', null, 'Create Webhook'), ' (or ', h('b', null, 'Webhooks'), ', then ', h('b', null, 'New Webhook'), ').'),
          h('li', null, 'Click the new webhook, name it (for example “Valheim”) and click ', h('b', null, 'Copy Webhook URL'), '.'),
          h('li', null, 'Paste it above, click ', h('b', null, 'Send a test'), ' and look in the channel. Then save.')),
        h('p', { class: 'small muted' },
          'Only people who manage the Discord server see Integrations: if you don’t, ask its owner for the link. Through the IP KVM, use its paste feature, or open Discord in a browser on this computer and copy the link there.')) : null,
      switchRow('Include the password',
        'Hidden as a spoiler until it’s clicked. Turning it on while the server is online posts the message again with it. Only turn this on if the channel is just for your friends.',
        d.password, (on) => setDiscord(game, 'password', on)),
      switchRow('Also post when the server stops', 'So nobody tries a join code that no longer works.',
        d.stopped, (on) => setDiscord(game, 'stopped', on)),
      switchRow('Post who died the most each day',
        'At 23:59, if anyone played that day. If the computer is off or resting then, it’s posted once it’s back.',
        d.daily, (on) => setDiscord(game, 'daily', on))) : null);
}

function automationSection(game) {
  const s = S(game);
  const mins = s.draft.autoStopMinutes;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Automatic')),
    valueSwitch(game, 'updateOnStart', 'Update the server before it starts',
      'Friends can’t join if their Valheim is newer than the server, so this checks Steam for updates each time.'),
    valueSwitch(game, 'autoRestart', 'Restart the server if it crashes', 'Gives up after 3 crashes in 15 minutes.'),
    switchRow('Start the server when this app opens', null, s.draft.startWithApp, (on) => {
      s.draft.startWithApp = on;
      view.render();
    }),
    switchRow('Stop the server when nobody is playing',
      'The world is saved first. It saves power, and memory for other games.', mins > 0, (on) => {
        s.draft.autoStopMinutes = on ? (base(s.view).autoStopMinutes || 60) : 0;
        delete s.errors.autoStopMinutes;
        view.render();
      }, {
        error: s.errors.autoStopMinutes,
        extra: mins > 0 ? h('div', { class: 'row', style: { marginTop: '8px' } }, 'After',
          h('input', {
            class: `input${s.errors.autoStopMinutes ? ' invalid' : ''}`, type: 'number', min: 1, max: 1440, value: String(mins), style: { width: '96px' }, 'aria-label': 'Minutes',
            oninput: (e) => { s.draft.autoStopMinutes = Number(e.target.value) || 0; delete s.errors.autoStopMinutes; view.render(); },
          }),
          'minutes without players') : null,
      }));
}

function backupSection(game) {
  const num = (name, attrs) => input(game, name, { type: 'number', ...attrs });
  const minutes = (name, attrs) => h('div', { class: 'input-group' }, num(name, attrs), h('span', { class: 'muted', style: { alignSelf: 'center' } }, 'minutes'));
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Saving & backups')),
    h('div', { class: 'field-row' },
      fieldFor(game, 'saveIntervalMinutes', 'Save the world every', minutes('saveIntervalMinutes', { min: 1, max: 240 }),
        'More often means less lost if the computer loses power. Each save causes a tiny hiccup.'),
      fieldFor(game, 'backups', 'Automatic backups to keep', num('backups', { min: 0, max: 50 }), 'Valheim’s own backups of the world.'),
      fieldFor(game, 'backupShortMinutes', 'First backup after', minutes('backupShortMinutes', { min: 5, max: 1440 })),
      fieldFor(game, 'backupLongMinutes', 'Then a backup every', minutes('backupLongMinutes', { min: 30, max: 10080 }))));
}

function openFolder(which) {
  api('/api/system/open-folder', { method: 'POST', body: { which } }).catch((e) => toast(e.message, 'bad', 9000));
}

function foldersSection(game) {
  const p = S(game).paths || {};
  const openBtn = (which) => h('button', { class: 'btn', type: 'button', onclick: () => openFolder(which) }, icon('folder'), 'Open');
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Folders & advanced')),
    h('div', { class: 'form' },
      fieldFor(game, 'serverDir', 'Valheim server folder', h('div', { class: 'input-group' }, input(game, 'serverDir', { placeholder: p.server }), openBtn('server')),
        'Leave empty to use the app’s own folder. Point this at an existing install (for example one from the Steam client) to reuse it.'),
      fieldFor(game, 'saveDir', 'Save folder (worlds, admin lists)', h('div', { class: 'input-group' }, input(game, 'saveDir', { placeholder: p.save }), openBtn('saves')),
        'Leave empty to use the app’s own folder.'),
      fieldFor(game, 'extraArgs', 'Extra launch options', input(game, 'extraArgs', { placeholder: 'e.g. -instanceid 2', spellcheck: 'false' }),
        'Added to the server’s command line. Only for advanced setups.'),
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-sm', type: 'button', onclick: () => openFolder('backups') }, icon('archive'), 'Backups folder'),
        h('button', { class: 'btn btn-sm', type: 'button', onclick: () => openFolder('logs') }, icon('terminal'), 'Logs folder'),
        h('button', { class: 'btn btn-sm', type: 'button', onclick: () => openFolder('data') }, icon('folder'), 'App folder'))));
}

async function install(game, repair) {
  if (repair && !await confirmDialog({ title: 'Repair the server files?', message: 'SteamCMD will check every server file and re-download anything damaged. This takes a few minutes. Your worlds are not touched.', confirm: 'Repair' })) return;
  try {
    await api(gameApi(game, '/install'), { method: 'POST', body: { repair } });
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  view.poll();
}

function maintenanceSection(game) {
  const running = game.state !== 'stopped' && game.state !== 'not_installed';
  const isBusy = busy(game);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Maintenance')),
    h('div', { class: 'row' },
      h('button', { class: 'btn', type: 'button', disabled: running || isBusy, onclick: () => install(game, false) }, icon('refresh'), 'Update the server now'),
      h('button', { class: 'btn', type: 'button', disabled: running || isBusy, onclick: () => install(game, true) }, icon('wrench'), 'Repair server files'),
      running ? h('span', { class: 'small muted' }, 'Stop the server to update or repair it.') : null));
}

async function setOff(game, off) {
  const name = game.short || game.name || 'this game';
  if (off && !await confirmDialog({
    title: `Turn ${name} off?`,
    message: `${name} can’t be started, from here or from Discord, until you turn it on again. If it’s running, the world is saved and it stops first. Nothing is deleted.`,
    confirm: 'Turn off', kind: 'danger', icon: 'power',
  })) return;
  try {
    await api(gameApi(game, '/settings'), { method: 'POST', body: { disabled: off } });
    toast(off ? `${name} is off.` : `${name} is on again.`, 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  await load(game, true);
  view.poll();
}

async function remove(game) {
  const name = game.short || game.name || 'this game';
  if (!await confirmDialog({
    title: `Remove ${name} from the panel?`,
    message: `${name} is stopped, unstarred, and taken out of the sidebar, Discord and the waker, so nothing starts it. Nothing is deleted: its server, worlds, backups and settings stay where they are, and adding ${name} again in Add a game brings it all back.`,
    confirm: 'Remove', kind: 'danger', icon: 'trash',
  })) return;
  try {
    await api(gameApi(game, '/remove'), { method: 'POST', body: {} });
    location.hash = '#/overview';
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  view.poll();
}

function gameSection(game) {
  const s = S(game);
  const name = game.short || game.name || 'this game';
  const off = !!s.view.disabled;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, `${name} on this computer`)),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, off ? `${name} is turned off` : `Turn ${name} off`),
        h('div', { class: 'help' }, off ? 'It can’t be started until you turn it on again.' : 'Nobody can start it, from here or from Discord, until you turn it on again.')),
      h('button', { class: `btn btn-sm${off ? ' btn-primary' : ''}`, type: 'button', onclick: () => setOff(game, !off) }, icon('power'), off ? 'Turn on' : 'Turn off')),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, `Remove ${name}`),
        h('div', { class: 'help' }, 'Takes it out of the panel. Its server, worlds and settings stay on this computer.')),
      h('button', { class: 'btn btn-sm btn-danger', type: 'button', onclick: () => remove(game) }, icon('trash'), 'Remove')));
}

// ---------- actions ----------

function newPassword(game) {
  // The same words as the app's own (config.go): two words and a number.
  const words = ['odin', 'thor', 'freya', 'loki', 'frigg', 'tyr', 'baldr', 'heimdall', 'fjord', 'rune', 'skald', 'longship',
    'mead', 'troll', 'draugr', 'yggdrasil', 'bifrost', 'valkyrie', 'jotunn', 'mjolnir', 'raven', 'wolf', 'drakkar', 'saga',
    'bronze', 'silver', 'serpent', 'eikthyr'];
  const pick = (n) => crypto.getRandomValues(new Uint32Array(1))[0] % n;
  setValue(game, 'password', `${words[pick(words.length)]}-${words[pick(words.length)]}-${1000 + pick(9000)}`);
}

async function testDiscord(game) {
  const s = S(game);
  s.testing = true;
  view.render();
  try {
    await api('/api/discord/test', { method: 'POST', body: { webhook: s.draft.discord.webhook.trim(), game: game.id } });
    delete s.errors['discord.webhook'];
    delete s.errors.discordWebhook;
    toast('Test message sent. Look in the Discord channel.', 'good');
  } catch (e) {
    const fields = errorsFrom(e.data && e.data.fields);
    Object.assign(s.errors, fields);
    toast(fields.discordWebhook || fields['discord.webhook'] || e.message, 'bad', 10000);
  } finally {
    s.testing = false;
    view.render();
  }
}

async function save(game) {
  const s = S(game);
  if (s.draft.discord.on && s.draft.discord.own && !s.draft.discord.webhook.trim()) {
    s.errors['discord.webhook'] = 'Paste the channel’s webhook link, or choose the main channel.';
    return view.render();
  }
  const body = change(s);
  if (!Object.keys(body).length) return;
  s.saving = true;
  view.render();
  try {
    const was = base(s.view);
    const discordNew = s.draft.discord.on && (!was.discord.on || 'webhook' in (body.discord || {}));
    await api(gameApi(game, '/settings'), { method: 'POST', body });
    const st = await api('/api/status').catch(() => null);
    const now = st && (st.games || []).find((g) => g.id === game.id);
    const online = now && now.state === 'online';
    toast(online && now.needsRestart ? 'Saved. Restart the server to use the new settings.'
      : online && discordNew ? 'Saved. The join code is being posted in Discord.' : 'Settings saved.', 'good');
    await load(game);
  } catch (e) {
    s.errors = errorsFrom(e.data && e.data.fields);
    toast(e.message, 'bad', 8000);
  } finally {
    s.saving = false;
    view.render();
  }
}

function onLeave(e) {
  if (shown && dirty(S(shown))) { e.preventDefault(); e.returnValue = ''; }
}

export const settingsTab = {
  title: 'Settings',
  render(app, game) {
    view.set(app);
    shown = game;
    const s = S(game);
    if (!s.view || !s.draft) return h('div', { class: 'vh' }, h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading settings…')));
    return h('div', { class: 'vh stack' },
      serverSection(game),
      discordSection(game),
      automationSection(game),
      backupSection(game),
      foldersSection(game),
      maintenanceSection(game),
      gameSection(game),
      dirty(s) ? h('div', { class: 'savebar', role: 'region', 'aria-label': 'Unsaved changes' },
        h('span', null, 'You have unsaved changes.'),
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn btn-ghost', type: 'button', onclick: () => { s.draft = structuredClone(base(s.view)); s.errors = {}; view.render(); } }, 'Discard'),
        h('button', { class: 'btn btn-primary', type: 'button', disabled: s.saving, onclick: () => save(game) }, icon('save'), s.saving ? 'Saving…' : 'Save changes')) : null);
  },
  mount(app, game) {
    view.set(app);
    shown = game;
    load(game, true);
    if (!S(game).paths) loadPaths(game);
    window.addEventListener('beforeunload', onLeave);
  },
  unmount() {
    window.removeEventListener('beforeunload', onLeave);
    shown = null;
  },
};
