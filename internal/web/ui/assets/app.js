// Valheim Easy Server control panel: app shell, live status and the actions
// every page shares.

import { h, morph, icon, api, toast, confirmDialog, copyText, dur, ago } from './dom.js';
import home from './home.js';
import worlds from './worlds.js';
import players from './players.js';
import stats from './stats.js';
import settings from './settings.js';
import logPage from './log.js';
import { initUploads, uploadStatus, ownsJob } from './upload.js';

const PAGES = [home, worlds, players, stats, settings, logPage];

export const app = {
  status: null,
  offline: false,
  page: home,
  actions: {},
  lastJob: null,
  dismissed: new Set(JSON.parse(sessionStorage.getItem('ves-dismissed') || '[]')),
  render,
  poll,
  go,
};

const root = document.getElementById('app');
let pollTimer = null;

// ---------- boot ----------
function boot() {
  root.className = '';
  initUploads(app);
  window.addEventListener('hashchange', route);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(); });
  route();
  poll();
}

// ---------- routing ----------
function route() {
  const id = (location.hash.replace(/^#\/?/, '') || 'home').split('?')[0];
  const next = PAGES.find((p) => p.id === id) || home;
  if (app.page !== next && app.page.unmount) app.page.unmount(app);
  const changed = app.page !== next;
  app.page = next;
  render(true);
  if (changed || !next._mounted) {
    next._mounted = true;
    if (next.mount) next.mount(app);
  }
  if (changed) window.scrollTo(0, 0);
}

function go(id) { location.hash = '#/' + id; }

// ---------- live status ----------
async function poll() {
  clearTimeout(pollTimer);
  try {
    const st = await api('/api/status');
    const wasOffline = app.offline;
    app.offline = false;
    onStatus(st);
    if (wasOffline) toast('Connected to the app again.', 'good');
  } catch {
    app.offline = true;
    render();
  }
  const busy = app.status && app.status.job && !app.status.job.finished;
  pollTimer = setTimeout(poll, document.hidden ? 10000 : busy ? 1000 : 2000);
}

function onStatus(st) {
  const job = st.job;
  // Report each job that finishes after the page loaded, even quick ones
  // that start and end between two refreshes.
  if (app.lastJob == null) app.lastJob = job ? (job.finished ? job.id : job.id - 1) : 0;
  const finished = job && job.finished && job.id > app.lastJob;
  if (finished) {
    app.lastJob = job.id;
    if (!(ownsJob(job.id) && app.page.id === 'worlds')) { // there, the Add a world section shows it
      if (job.error) toast(job.error, 'bad', 10000);
      else toast(job.result || `${job.title}: done.`, 'good');
    }
  }
  app.status = st;
  uploadStatus(st);
  render();
  if (finished && app.page.onJobDone) app.page.onJobDone(app, job);
}

// ---------- rendering ----------
export function stateInfo(st) {
  if (!st) return { label: 'Connecting…', dot: '' };
  if (st.job && !st.job.finished && st.job.kind === 'install') return { label: 'Updating…', dot: 'warn' };
  switch (st.state) {
    case 'online': return { label: 'Online', dot: 'good' };
    case 'starting': return { label: 'Starting…', dot: 'warn' };
    case 'stopping': return { label: 'Stopping…', dot: 'warn' };
    case 'not_installed': return { label: 'Not installed', dot: '' };
  }
  if (st.lastExit && !st.lastExit.expected && Date.now() - new Date(st.lastExit.at) < 3600e3) return { label: 'Crashed', dot: 'bad' };
  return { label: 'Offline', dot: '' };
}

function sidebar() {
  const st = app.status;
  const players = st ? st.players.length : 0;
  return h('aside', { class: 'sidebar' },
    h('div', { class: 'brand' },
      h('img', { src: '/assets/logo.svg', alt: '' }),
      h('div', null, h('b', null, 'Valheim Easy Server'), h('span', null, st ? st.serverName : ''))),
    h('nav', { class: 'nav', 'aria-label': 'Pages' },
      PAGES.map((p) => h('a', { href: '#/' + p.id, class: app.page === p ? 'active' : '', 'aria-current': app.page === p ? 'page' : null },
        icon(p.icon), p.title,
        p.id === 'players' && players ? h('span', { class: 'count', 'aria-label': `${players} online` }, players) : null))),
    h('div', { class: 'sidebar-foot' },
      st && st.machine ? h('div', null, 'Server computer: ', st.machine.hostname) : null));
}

function topbar() {
  const st = app.status;
  const info = stateInfo(st);
  const hibernating = st && st.job && !st.job.finished && st.job.kind === 'hibernate';
  return h('div', { class: 'topbar' },
    h('h1', null, app.page.heading || app.page.title),
    h('span', { class: 'status-pill', role: 'status' }, h('span', { class: `dot ${info.dot}` }), info.label,
      st && st.state === 'online' ? h('span', { class: 'muted' }, `· ${st.players.length} playing`) : null),
    h('button', { class: 'btn', 'data-action': 'hibernate', disabled: !st || hibernating, title: 'Save the world, stop the server and put the computer to sleep' },
      icon('moon'), 'Hibernate'));
}

function banners() {
  const out = [];
  if (app.offline) {
    out.push(h('div', { class: 'offline-banner', role: 'alert' }, icon('alert'),
      h('div', null, h('b', null, 'Lost contact with the app. '),
        'If it was closed, start ValheimEasyServer.exe again. This page reconnects by itself.')));
  }
  return out;
}

export function render(force) {
  const tree = h('div', { class: 'shell' },
    sidebar(),
    h('main', { class: 'main', id: 'main' },
      h('div', { class: 'page' }, ...banners(), topbar(),
        h('div', { id: 'content' }, app.page.render(app)))));
  const current = root.firstElementChild;
  if (!current || force && current.getAttribute('data-page') !== app.page.id) {
    tree.setAttribute('data-page', app.page.id);
    root.replaceChildren(tree);
  } else {
    tree.setAttribute('data-page', app.page.id);
    morph(current, tree);
  }
  if (app.page.afterRender) app.page.afterRender(app);
}

// ---------- shared actions ----------
const A = app.actions;

async function run(path, body, okMsg) {
  try {
    await api(path, { method: 'POST', body: body || {} });
    if (okMsg) toast(okMsg, 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
    if (e.data && e.data.fields && app.page.id !== 'settings') go('settings');
  }
  poll();
}

function playersWarning(verb) {
  const st = app.status;
  const n = st ? st.players.length : 0;
  if (!n) return null;
  const names = st.players.map((p) => p.name).join(', ');
  return `${n === 1 ? '1 person is' : n + ' people are'} playing right now (${names}). They'll be disconnected when you ${verb}.`;
}

A.start = () => run('/api/server/start');
A.stop = async () => {
  const st = app.status;
  const warn = playersWarning('stop the server');
  if (st && st.state === 'starting') {
    if (!await confirmDialog({ title: 'Stop while loading?', message: 'The server is still loading the world. After a Valheim update this can include converting the world, which is best not interrupted. Stop anyway?', confirm: 'Stop server', kind: 'danger' })) return;
  } else if (warn && !await confirmDialog({ title: 'Stop the server?', message: warn + ' The world is saved first.', confirm: 'Save and stop', kind: 'danger', icon: 'stop' })) return;
  run('/api/server/stop');
};
A.restart = async () => {
  const warn = playersWarning('restart');
  if (warn && !await confirmDialog({ title: 'Restart the server?', message: warn + ' The world is saved first and the server is back in about a minute.', confirm: 'Restart', icon: 'restart' })) return;
  run('/api/server/restart');
};
A['force-stop'] = async () => {
  const st = app.status;
  const last = st && st.lastSave ? ` The last save was ${ago(st.lastSave)}.` : '';
  if (!await confirmDialog({ title: 'Force the server to stop?', message: `This ends the server immediately without saving. Anything since the last save is lost.${last}`, confirm: 'Force stop', kind: 'danger' })) return;
  run('/api/server/force-stop', null, 'The server was forced to stop.');
};
A.install = () => run('/api/server/install', { repair: false });
A.repair = async () => {
  if (!await confirmDialog({ title: 'Repair the server files?', message: 'SteamCMD will check every server file and re-download anything damaged. This takes a few minutes. Your worlds are not touched.', confirm: 'Repair' })) return;
  run('/api/server/install', { repair: true });
};
A['cancel-job'] = () => run('/api/job/cancel');
A.hibernate = async () => {
  const st = app.status;
  const warn = playersWarning('hibernate');
  const msg = h('div', { class: 'stack', style: { gap: '10px' } },
    h('p', null, st && st.state !== 'stopped' && st.state !== 'not_installed'
      ? 'The world is saved and the server stops first. Then the computer goes into hibernation, using almost no power.'
      : 'The computer goes into hibernation, using almost no power.'),
    warn ? h('p', null, h('strong', null, warn)) : null,
    h('p', null, h('strong', null, 'To wake it up, someone has to press the power button on the computer.')));
  if (!await confirmDialog({ title: 'Hibernate this computer?', message: msg, confirm: 'Hibernate', icon: 'moon' })) return;
  run('/api/power/hibernate');
};
A['keep-awake'] = () => run('/api/power/keep-awake', null, 'Automatic hibernation postponed.');
A['enable-hibernate'] = () => run('/api/power/enable-hibernate', null, 'Windows will ask for permission. Click Yes.');
A['fix-firewall'] = () => run('/api/system/firewall', null, 'Windows will ask for permission. Click Yes, then wait a few seconds.');
A['open-folder'] = (d) => run('/api/system/open-folder', { which: d.which });
A.copy = (d) => copyText(d.text, d.label || 'Copied');
A.go = (d) => go(d.page);
A['dismiss-job'] = (d) => {
  app.dismissed.add(Number(d.id));
  sessionStorage.setItem('ves-dismissed', JSON.stringify([...app.dismissed]));
  render();
};
A['show-log'] = () => go('log');
A['show-settings'] = () => go('settings');
A.reveal = (d, el) => {
  const target = document.getElementById(d.target);
  if (!target) return;
  const shown = target.dataset.shown === '1';
  target.dataset.shown = shown ? '0' : '1';
  target.textContent = shown ? '•'.repeat(Math.min(12, d.secret.length)) : d.secret;
  el.replaceChildren(icon(shown ? 'eye' : 'eyeOff'));
  el.setAttribute('aria-label', shown ? 'Show password' : 'Hide password');
};

// Click and change handling by data-action, so re-rendered parts of the
// page keep working without re-attaching listeners.
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

export function jobLine(job) {
  const pct = job.percent;
  const known = pct >= 0 && pct <= 100;
  return h('div', { class: 'jobline', role: 'status' },
    h('div', { class: 'row' }, h('b', null, job.title), h('span', { class: 'muted small' }, dur((Date.now() - new Date(job.started)) / 1000)),
      h('span', { class: 'spacer' }),
      known ? h('span', { class: 'small text-2' }, Math.round(pct) + '%') : null,
      job.cancellable ? h('button', { class: 'btn btn-sm', 'data-action': 'cancel-job' }, 'Cancel') : null),
    job.message ? h('div', { class: 'small text-2', style: { marginTop: '4px' } }, job.message) : null,
    h('div', { class: `progress${known ? '' : ' indeterminate'}` }, h('i', { style: { width: (known ? pct : 35) + '%' } })));
}

boot();
