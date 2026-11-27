// The control panel's shell (spec 6.1): the sidebar with the games in two
// folding lists (Active and Inactive, with stars), the top bar, the pages'
// addresses, the live status, and the actions every page shares. The pages
// themselves are in their own modules; each game's own parts are in
// assets/games/<id>.js, loaded by games.js. The shell never names a game:
// everything about the games comes from the status.

import { h, morph, icon, api, toast, confirmDialog, copyText, setReopenHint } from './dom.js';
import { load as loadModules, whenLoaded } from './games.js';
import { gameState, isRunning, tile, legacyID, gameByID, listNames, countWord, enc } from './state.js';
import { jobLine, restWord, isMac as macStatus, askNote } from './parts.js';
import { startGame } from './memory.js';
import overview from './overview.js';
import addGame from './addgame.js';
import { gamePage, TABS } from './gamepage.js';
import discordPage from './app-discord.js';
import sleepPage from './app-sleep.js';
import settingsPage from './app-settings.js';

// The name people see. (Stream T renames the rest of the app: the page's
// title, the manifest, the tray and the texts that send the owner to the
// app on the server computer, which keep its installed name.)
export const APP_NAME = 'Easy Game Server';

export const app = {
  status: null,
  offline: false,
  route: { view: 'overview' },
  page: null,
  pageKey: '',
  actions: {},
  navOpen: false,
  fieldErrors: {},          // by instance: problems a start or save found, shown on its Settings tab
  revealed: new Set(),      // instances whose password is shown on How friends join
  dismissed: new Set(readDismissed()),
  render,
  poll,
  go,
  showFieldErrors,
};

// Pages that aren't a game's, by route.
const APP_PAGES = { discord: discordPage, sleep: sleepPage, settings: settingsPage };
// The old panel's addresses: each is now that tab of the legacy game.
const OLD_PAGES = ['home', 'worlds', 'players', 'stats', 'settings', 'log'];

const root = document.getElementById('app');
let pollTimer = null;
let polling = null;
const seenJobs = new Set();
let firstStatus = true;
// Changes the owner just made, shown at once and until the app confirms
// them, so the sidebar doesn't flick back while the request is on its way.
const starWanted = new Map();
const sideWanted = {};

function readDismissed() {
  try { return JSON.parse(sessionStorage.getItem('ves-dismissed') || '[]'); } catch { return []; }
}

// ---------- boot ----------
function boot() {
  root.className = '';
  whenLoaded(() => render());
  window.addEventListener('hashchange', route);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && app.navOpen) closeNav(); });
  route();
  poll();
}

// ---------- routing ----------
// parseHash reads the page's address: #/overview (the default),
// #/game/<instance>/<tab>, #/add, #/add/<game>, #/app/discord,
// #/app/sleep, #/app/settings; and the old panel's #/home, #/worlds, …
function parseHash() {
  let parts;
  try {
    parts = location.hash.replace(/^#\/?/, '').split('?')[0].split('/').map(decodeURIComponent);
  } catch {
    return { view: 'overview' };
  }
  const [a = '', b = '', c = ''] = parts;
  switch (a) {
    case '':
    case 'overview':
      return { view: 'overview' };
    case 'game':
      return b ? { view: 'game', id: b, tab: TABS.includes(c) ? c : 'home' } : { view: 'overview' };
    case 'add':
      return { view: 'add', game: b };
    case 'app':
      return { view: 'app', page: APP_PAGES[b] ? b : 'settings' };
  }
  if (OLD_PAGES.includes(a)) return { view: 'old', tab: a };
  return { view: 'overview' };
}

function routeKey(r) {
  switch (r.view) {
    case 'game': return `game/${r.id}/${r.tab}`;
    case 'add': return r.game ? `add/${r.game}` : 'add';
    case 'app': return `app/${r.page}`;
    default: return 'overview';
  }
}

function pageFor(r) {
  switch (r.view) {
    case 'game': return gamePage(r.id, r.tab);
    case 'add': return addGame;
    case 'app': return APP_PAGES[r.page];
    default: return overview;
  }
}

function route() {
  const r = parseHash();
  if (r.view === 'old') {
    // Wait for the status, which says which game the old pages were for.
    if (!app.status) return;
    location.replace(`#/game/${enc(legacyID(app.status))}/${r.tab}`);
    return;
  }
  const key = routeKey(r);
  const changed = key !== app.pageKey;
  if (changed && app.page && app.page.unmount) app.page.unmount(app);
  app.route = r;
  if (changed) {
    app.page = pageFor(r);
    app.pageKey = key;
  }
  app.navOpen = false;
  render();
  if (changed) {
    if (app.page.mount) app.page.mount(app);
    window.scrollTo(0, 0);
  }
}

export function go(path) { location.hash = '#/' + path; }

// ---------- live status ----------
// freshPoll asks for a status that is sure to be newer than a change just
// made (a look already on its way may have left before it).
async function freshPoll() {
  if (polling) await polling;
  return poll();
}

export function poll() {
  clearTimeout(pollTimer);
  if (polling) return polling;
  polling = (async () => {
    try {
      const st = await api('/api/status');
      const wasOffline = app.offline;
      app.offline = false;
      onStatus(st);
      if (wasOffline) toast('Connected to the app again.', 'good');
    } catch {
      app.offline = true;
      render();
    } finally {
      polling = null;
      const busy = app.status && (app.status.jobs || []).some((j) => !j.finished);
      pollTimer = setTimeout(poll, document.hidden ? 10000 : busy ? 1000 : 2000);
    }
  })();
  return polling;
}

function onStatus(st) {
  // After the app has updated itself, load the new version of this page.
  if (st.app && app.appVersion && st.app.version !== app.appVersion) {
    location.reload();
    return;
  }
  if (st.app) app.appVersion = st.app.version;
  st.games = st.games || [];
  setReopenHint(reopenHint(st));
  loadModules(st.games.map((g) => g.game));
  const done = finishedJobs(st);
  app.status = st;
  for (const [id, on] of starWanted) {
    const g = gameByID(st, id);
    if (!g || g.starred === on.on && on.sent) starWanted.delete(id);
  }
  if (parseHash().view === 'old') route(); // now it's known which game the old address means
  render();
  for (const job of done) {
    if (app.page && app.page.onJobDone) app.page.onJobDone(app, job);
  }
}

// finishedJobs reports each job that finished since the page loaded, even
// quick ones that start and end between two looks at the status.
function finishedJobs(st) {
  const all = [...(st.jobs || [])];
  if (st.job) all.push(st.job);
  for (const g of st.games) if (g.job) all.push(g.job);
  const out = [];
  for (const job of all) {
    if (!job.finished || seenJobs.has(job.id)) continue;
    seenJobs.add(job.id);
    if (firstStatus) continue;
    out.push(job);
    if (app.page && app.page.ownsJob && app.page.ownsJob(job)) continue; // that page shows it itself
    const g = job.game ? gameByID(st, job.game) : null;
    const who = g && !job.title.includes(g.short) ? `${g.short}: ` : '';
    if (job.error) toast(who + job.error, 'bad', 10000);
    else toast(who + (job.result || `${job.title}: done.`), 'good');
  }
  firstStatus = false;
  return out;
}

// reopenHint says how to open the app again on the server computer, by
// the name it has there (on a Mac, the one in the Applications folder).
function reopenHint(st) {
  const platform = st && st.platform;
  if (platform === 'darwin') return 'open Valheim Easy Server again (for example from the Applications folder)';
  if (platform === 'linux') return 'start the app again';
  return 'start ValheimEasyServer.exe again';
}

// ---------- the sidebar ----------
const starOf = (g) => (starWanted.has(g.id) ? starWanted.get(g.id).on : !!g.starred);

function sideOpen() {
  const s = (app.status && app.status.sidebar) || { activeOpen: true, inactiveOpen: false };
  return {
    activeOpen: 'activeOpen' in sideWanted ? sideWanted.activeOpen : s.activeOpen !== false,
    inactiveOpen: 'inactiveOpen' in sideWanted ? sideWanted.inactiveOpen : !!s.inactiveOpen,
  };
}

// lists splits the games: Active is the starred games, then running ones
// without a star (only while they run); Inactive is the rest.
export function lists(st) {
  const games = (st && st.games) || [];
  const starred = games.filter(starOf);
  const running = games.filter((g) => !starOf(g) && isRunning(g));
  return { active: [...starred, ...running], starred, running, inactive: games.filter((g) => !starOf(g) && !isRunning(g)) };
}

function navLink(href, ic, label, here, cls) {
  return h('a', { href, class: `${cls || ''}${here ? ' active' : ''}`.trim() || null, 'aria-current': here ? 'page' : null }, icon(ic), h('span', { class: 'grow' }, label));
}

function twirl(list, label, n, open) {
  return h('button', { class: 'twirl', type: 'button', 'data-action': 'twirl', 'data-list': list, 'aria-expanded': String(open), 'aria-controls': 'list-' + list },
    icon('chevronDown'), label, h('span', { class: 'n' }, `(${n})`));
}

export function starButton(g, extra) {
  const on = starOf(g);
  return h('button', {
    class: `starbtn${on ? ' on' : ''}${extra ? ' ' + extra : ''}`, type: 'button', 'data-action': 'star', 'data-id': g.id, 'aria-pressed': String(on),
    'aria-label': `${on ? 'Unstar' : 'Star'} ${g.short}`, title: on ? 'Starred: you’re playing this now. Click to unstar.' : 'Star it: you’re playing this now',
  }, icon('star'));
}

function gameRow(g) {
  const r = app.route;
  const here = r.view === 'game' && r.id === g.id;
  const state = gameState(g);
  // Switching games keeps the tab, except Mods, which not every game has.
  const tab = r.view === 'game' && r.tab !== 'mods' ? r.tab : 'home';
  let tail = null;
  if (g.state === 'online') tail = h('span', { class: 'count live', title: `${g.count >= 0 ? g.count : 'Some'} playing` }, g.count >= 0 ? String(g.count) : '•');
  else if (state.dot === 'warn' || state.dot === 'bad') tail = h('span', { class: `dot ${state.dot}`, title: state.label });
  return h('div', { class: `nav-game${here ? ' active' : ''}`, 'data-key': 'g:' + g.id },
    h('a', { href: `#/game/${enc(g.id)}/${tab}`, 'aria-current': here ? 'page' : null, title: `${g.name}: ${state.label}` },
      tile(g), h('span', { class: 'grow' }, g.short), tail),
    starButton(g));
}

function sidebar() {
  const st = app.status;
  const r = app.route;
  const { active, inactive } = lists(st);
  const open = sideOpen();
  return h('aside', { class: 'sidebar', id: 'sidebar', 'aria-label': 'Menu' },
    h('div', { class: 'brand' },
      h('img', { src: '/assets/logo.svg', alt: '' }),
      h('div', null, h('b', null, APP_NAME), h('span', null, st ? countWord(st.games.length, 'game', 'games') : ''))),
    h('nav', { class: 'nav', 'aria-label': 'Pages' },
      navLink('#/overview', 'grid', 'Overview', r.view === 'overview'),
      twirl('active', 'Active', active.length, open.activeOpen),
      open.activeOpen ? h('div', { class: 'nav-games', id: 'list-active' },
        active.length ? active.map(gameRow) : h('p', { class: 'nav-empty' }, 'Star the games you’re playing now and they stay here.')) : null,
      twirl('inactive', 'Inactive', inactive.length, open.inactiveOpen),
      open.inactiveOpen ? h('div', { class: 'nav-games', id: 'list-inactive' },
        inactive.length ? inactive.map(gameRow) : h('p', { class: 'nav-empty' }, 'Every game is active.')) : null,
      navLink('#/add', 'plus', 'Add a game', r.view === 'add', 'add'),
      h('div', { class: 'nav-group' }, 'App'),
      navLink('#/app/discord', 'message', 'Discord', r.view === 'app' && r.page === 'discord'),
      navLink('#/app/sleep', 'moon', 'Sleep and waking', r.view === 'app' && r.page === 'sleep'),
      navLink('#/app/settings', 'sliders', 'App settings', r.view === 'app' && r.page === 'settings')),
    h('div', { class: 'sidebar-foot' }, st && st.machine ? `Server computer: ${st.machine.hostname}` : ''));
}

// ---------- the top bar ----------
function topbar() {
  const st = app.status;
  const online = st ? st.online || 0 : 0;
  const playing = st ? st.playing || 0 : 0;
  const resting = st && (st.jobs || []).some((j) => !j.finished && j.kind === 'hibernate');
  const heading = app.page && app.page.heading ? app.page.heading(app) : (app.page && app.page.title) || '';
  return h('div', { class: 'topbar' },
    h('button', { class: 'btn btn-ghost btn-icon nav-toggle', type: 'button', 'data-action': 'nav-toggle', 'aria-label': 'Menu', 'aria-expanded': String(app.navOpen), 'aria-controls': 'sidebar' }, icon('menu')),
    h('h1', null, heading),
    h('span', { class: 'status-pill', role: 'status' }, h('span', { class: `dot ${online ? 'good' : ''}` }),
      st ? `${countWord(online, 'game', 'games')} online` : 'Connecting…', st ? h('span', { class: 'muted' }, `· ${playing} playing`) : null),
    h('button', { class: 'btn', type: 'button', 'data-action': 'sleep', disabled: !st || resting, title: 'Save and stop every game, then let the computer rest' },
      icon('moon'), h('span', { class: 'label' }, restWord(st))));
}

function banners() {
  const out = [];
  const st = app.status;
  if (app.offline) {
    out.push(h('div', { class: 'offline-banner', role: 'alert' }, icon('alert'),
      h('div', null, h('b', null, 'Lost contact with the app. '),
        `If it was closed, ${reopenHint(st)}. This page reconnects by itself.`)));
  }
  if (st && st.safeMode) {
    out.push(h('div', { class: 'callout warn banner', role: 'alert' }, icon('alert'), h('div', { class: 'body' }, h('p', null, st.safeMode))));
  }
  return out;
}

// ---------- drawing ----------
function content() {
  if (!app.page) return null;
  if (!app.status) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Waiting for the app…'));
  try {
    return app.page.render(app);
  } catch (e) {
    console.error(e);
    return h('div', { class: 'callout bad' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, h('strong', null, 'This page couldn’t be drawn. ')), h('p', { class: 'small' }, String(e && e.message || e))));
  }
}

export function render() {
  if (!app.page) return;
  if (!app.status && !app.offline) {
    // Until the first status the boot screen stays.
    return;
  }
  const tree = h('div', { class: `shell${app.navOpen ? ' nav-open' : ''}`, 'data-page': app.pageKey },
    sidebar(),
    h('div', { class: 'nav-scrim', 'data-action': 'nav-close', 'aria-hidden': 'true' }),
    h('main', { class: 'main', id: 'main' },
      h('div', { class: 'page' }, ...banners(), topbar(), h('div', { id: 'content' }, content()))));
  const current = root.firstElementChild;
  if (!current || !current.classList.contains('shell') || current.getAttribute('data-page') !== app.pageKey) root.replaceChildren(tree);
  else morph(current, tree);
  if (app.page.afterRender) {
    try { app.page.afterRender(app); } catch (e) { console.error(e); }
  }
}

function closeNav() {
  app.navOpen = false;
  render();
}

// showFieldErrors opens a game's Settings tab with the problems a start or
// a save found, by field ("server.password": "…").
function showFieldErrors(id, fields) {
  app.fieldErrors[id] = { ...(app.fieldErrors[id] || {}), ...fields };
  if (!(app.route.view === 'game' && app.route.id === id && app.route.tab === 'settings')) go(`game/${enc(id)}/settings`);
  else render();
}

// ---------- shared actions ----------
const A = app.actions;
const gameOf = (d) => gameByID(app.status, d.id);

// run sends a change and reports a problem. id is the instance it was for,
// so a field problem can be shown on its Settings tab.
export async function run(path, body, okMsg, id) {
  let ok = false;
  try {
    await api(path, { method: 'POST', body: body || {} });
    ok = true;
    if (okMsg) toast(okMsg, 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
    if (id && e.data && e.data.fields) showFieldErrors(id, e.data.fields);
  }
  poll();
  return ok;
}

// playersWarning names who is playing a game, before something
// disconnects them.
function playersWarning(g, verb) {
  const names = (g.players || []).map((p) => p.name);
  const n = g.count > 0 ? g.count : names.length;
  if (!n) return null;
  return `${n === 1 ? '1 person is' : n + ' people are'} playing ${g.short} right now${names.length ? ` (${names.join(', ')})` : ''}. They’ll be disconnected when you ${verb}.`;
}

A['game-start'] = (d) => { const g = gameOf(d); if (g) startGame(app, g); };
A['game-stop'] = async (d) => {
  const g = gameOf(d);
  if (!g) return;
  const warn = playersWarning(g, 'stop it');
  if (g.state === 'starting') {
    if (!await confirmDialog({ title: `Stop ${g.short} while it loads?`, message: 'The server is still loading its world. A first start or an update can take a few minutes and is best not interrupted. Stop anyway?', confirm: 'Stop', kind: 'danger' })) return;
  } else if (warn && !await confirmDialog({ title: `Stop ${g.short}?`, message: warn + ' The world is saved first.', confirm: 'Save and stop', kind: 'danger', icon: 'stop' })) return;
  run(`/api/games/${enc(g.id)}/stop`, null, null, g.id);
};
A['game-restart'] = async (d) => {
  const g = gameOf(d);
  if (!g) return;
  const warn = playersWarning(g, 'restart it');
  if (warn && !await confirmDialog({ title: `Restart ${g.short}?`, message: warn + ' The world is saved first and the server is back in a minute or two.', confirm: 'Restart', icon: 'restart' })) return;
  run(`/api/games/${enc(g.id)}/restart`, null, null, g.id);
};
A['game-force-stop'] = async (d) => {
  const g = gameOf(d);
  if (!g) return;
  if (!await confirmDialog({ title: `Force ${g.short} to stop?`, message: 'This ends the server at once without saving. Anything since its last save is lost.', confirm: 'Force stop', kind: 'danger' })) return;
  run(`/api/games/${enc(g.id)}/force-stop`, null, `${g.short} was forced to stop.`, g.id);
};
A['game-cancel'] = (d) => run(`/api/games/${enc(d.id)}/cancel`);
A['game-install'] = (d) => run(`/api/games/${enc(d.id)}/install`, { repair: false }, null, d.id);
A['game-repair'] = async (d) => {
  const g = gameOf(d);
  if (!g) return;
  if (!await confirmDialog({ title: `Repair ${g.short}’s server files?`, message: 'Every server file is checked and anything damaged is downloaded again. This can take a few minutes. Worlds are not touched.', confirm: 'Repair' })) return;
  run(`/api/games/${enc(g.id)}/install`, { repair: true }, null, g.id);
};
A['keep-on'] = (d) => run(`/api/games/${enc(d.id)}/keep-on`, null, 'It stays on.', d.id);
A.allow = (d) => run(`/api/games/${enc(d.id)}/allow`, { name: d.name }, `${d.name} can join from now on.`, d.id);
A['go-tab'] = (d) => go(`game/${enc(d.id)}/${d.tab}`);

A.star = async (d) => {
  const g = gameOf(d);
  if (!g) return;
  const want = { on: !starOf(g), sent: false };
  starWanted.set(g.id, want);
  render();
  try {
    await api(`/api/games/${enc(g.id)}/star`, { method: 'POST', body: { on: want.on } });
    want.sent = true;
  } catch (e) {
    toast(e.message, 'bad', 8000);
  }
  await freshPoll();
  starWanted.delete(g.id);
  render();
};
A.twirl = async (d) => {
  const key = d.list === 'inactive' ? 'inactiveOpen' : 'activeOpen';
  const open = !sideOpen()[key];
  sideWanted[key] = open;
  render();
  const btn = document.querySelector(`[data-action="twirl"][data-list="${d.list}"]`);
  if (btn) btn.focus();
  try {
    await api('/api/app/sidebar', { method: 'POST', body: { [key]: open } });
    await freshPoll();
  } catch (e) {
    toast(e.message, 'bad', 8000);
  }
  delete sideWanted[key];
  render();
};

A.sleep = async () => {
  const st = app.status;
  const running = st.games.filter(isRunning);
  const word = restWord(st);
  const people = running.reduce((n, g) => n + Math.max(0, g.count || 0), 0);
  const msg = h('div', { class: 'stack', style: { gap: '10px' } },
    h('p', null, running.length
      ? `${listNames(running.map((g) => g.short))} ${running.length === 1 ? 'is' : 'are'} saved and stopped first. `
      : 'No game is running. ',
    word === 'Sleep' ? 'Then the computer goes to sleep, using very little power.' : 'Then the computer hibernates, using no power.'),
    people ? h('p', null, h('strong', null, `${people === 1 ? '1 person is' : people + ' people are'} playing right now. They’ll be disconnected.`)) : null,
    h('p', null, 'Wake it with Wake-on-LAN (for example from the IP KVM), from Discord, or with its power button.'));
  if (!await confirmDialog({ title: word === 'Sleep' ? 'Put this computer to sleep?' : 'Hibernate this computer?', message: msg, confirm: word, icon: 'moon' })) return;
  run('/api/power/hibernate');
};
A['keep-awake'] = () => run('/api/power/keep-awake', null, 'Resting automatically was put off.');
A['enable-wol'] = () => run('/api/power/enable-wake-on-lan', null, askNote(app.status));
A['enable-hibernate'] = () => run('/api/power/enable-hibernate', null, 'Windows will ask for permission. Click Yes.');
A['fix-firewall'] = () => run('/api/system/firewall', null, askNote(app.status));
A['open-folder'] = (d) => run('/api/system/open-folder', { which: d.which });
A['router-check'] = () => run('/api/router/check', null, 'Checking the router again…');
A.copy = (d) => copyText(d.text, d.label || 'Copied');
A.go = (d) => go(d.page);
A.reveal = (d) => {
  if (app.revealed.has(d.id)) app.revealed.delete(d.id);
  else app.revealed.add(d.id);
  render();
};
A['dismiss-job'] = (d) => {
  app.dismissed.add(Number(d.id));
  try { sessionStorage.setItem('ves-dismissed', JSON.stringify([...app.dismissed])); } catch { /* only for this page */ }
  render();
};
A['nav-toggle'] = () => {
  app.navOpen = !app.navOpen;
  render();
  if (app.navOpen) {
    const first = document.querySelector('#sidebar a, #sidebar button');
    if (first) first.focus();
  }
};
A['nav-close'] = closeNav;

// Click and change handling by data-action, so re-drawn parts of the page
// keep working without re-attaching listeners.
document.addEventListener('click', (e) => {
  const el = e.target.closest('[data-action]');
  if (!el || el.disabled || el.tagName === 'INPUT' || el.tagName === 'SELECT') return;
  const fn = A[el.dataset.action];
  if (!fn) return;
  e.preventDefault();
  fn(el.dataset, el, e);
});
document.addEventListener('change', (e) => {
  const el = e.target.closest('[data-action]');
  if (!el || (el.tagName !== 'INPUT' && el.tagName !== 'SELECT')) return;
  const fn = A[el.dataset.action];
  if (fn) fn(el.dataset, el, e);
});
// For the old pages' modules, which stay in the tree until the Valheim
// module has taken over what they did (they import these).
export const stateInfo = gameState;
export const isMac = () => macStatus(app.status);
export { jobLine, restWord, tile };

boot();
