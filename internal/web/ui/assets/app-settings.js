// App settings (APP group): the app's own updates, starting it at login,
// router ports for every game (spec 6.5), the firewall, the whole
// computer's charts (each game's own are on its Stats tab), folders, and
// closing the app.

import { h, icon, api, toast, confirmDialog, toggle, ago, fmtBytes, fmtPct, fmtRate } from './dom.js';
import { LineChart, Crosshair } from './charts.js';
import { tile, gameByID, isRunning, routerOpen } from './state.js';
import { callout, isMac } from './parts.js';
import { APP_NAME } from './app.js';
import { portLabel, loadCatalog } from './catalog.js';

let appRef = null;
let view = null;   // GET /api/settings
let router = null; // GET /api/router
let stats = null;  // GET /api/stats (the whole computer)
let checking = false;
let saving = '';
let charts = {};
let crosshair = null;
let timer = null;

const here = () => appRef && appRef.route.view === 'app' && appRef.route.page === 'settings';
const redraw = () => { if (here()) appRef.render(); };

async function load() {
  try {
    [view, router] = await Promise.all([api('/api/settings'), api('/api/router').catch(() => null)]);
  } catch (e) {
    toast(e.message, 'bad');
  }
  redraw();
}

async function loadStats() {
  try {
    stats = await api('/api/stats?range=1h');
  } catch { /* the charts wait */ }
  redraw();
}

async function save(change, okMsg) {
  saving = Object.keys(change)[0];
  redraw();
  try {
    await api('/api/app/settings', { method: 'POST', body: change });
    if (okMsg) toast(okMsg, 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  } finally {
    saving = '';
    await load();
    appRef.poll();
  }
}

function updatesCard(app) {
  const a = (app.status && app.status.app) || {};
  let line;
  let button = null;
  if (!a.build) {
    line = `This copy (${view.version}) doesn’t update itself. Copies downloaded from GitHub do.`;
  } else if (a.updating) {
    line = `Updating to build ${a.latest ? a.latest.build : ''}… The panel reconnects by itself in a few seconds.`;
  } else if (a.failed > a.build && (!a.latest || a.latest.build === a.failed)) {
    line = `Build ${a.failed} didn’t start on this computer, so the app went back to build ${a.build}. It won’t install build ${a.failed} by itself.`;
    button = h('button', { class: 'btn btn-sm', 'data-action': 'app-update', 'data-install': '1' }, icon('refresh'), 'Try again');
  } else if (a.latest) {
    line = [h('strong', null, `Build ${a.latest.build} is ready`), a.latest.title ? `: ${a.latest.title}` : '.'];
    button = h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'app-update', 'data-install': '1' }, icon('download'), 'Update now');
  } else if (a.error) {
    line = `Couldn’t check for updates: ${a.error}`;
  } else if (a.checkedAt) {
    line = `Up to date (build ${a.build}). Checked ${ago(a.checkedAt)}.`;
  } else {
    line = `Build ${a.build}. It looks for updates a couple of minutes after it starts.`;
  }
  if (a.build && !button && !a.updating) {
    button = h('button', { class: 'btn btn-sm', 'data-action': 'app-update', disabled: checking }, icon('refresh'), checking ? 'Checking…' : 'Check now');
  }
  const mac = isMac(app.status);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('refresh'), h('h2', null, 'The app')),
    a.build ? h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Update this app by itself'),
        h('div', { class: 'help' }, 'Every hour it checks GitHub for a newer version and installs it when nothing is under way. Games keep running while the app restarts, so nobody is disconnected.')),
      toggle({ checked: !!view.settings.autoUpdate, 'data-action': 'app-auto-update', 'aria-label': 'Update this app by itself', disabled: !!saving })) : null,
    h('div', { class: `row update-row${a.build ? ' ruled' : ''}` }, h('p', { class: 'grow text-2' }, line), button),
    h('div', { class: 'toggle-row ruled-top' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, mac ? 'Start this app when you log in' : 'Start this app when Windows starts'),
        h('div', { class: 'help' }, mac ? 'Opens quietly in the background. Open Valheim Easy Server again from the Applications folder to see the control panel.' : 'Opens quietly in the background (look for its icon in the taskbar corner).')),
      toggle({ checked: !!view.launchOnLogin, 'data-action': 'app-login', 'aria-label': mac ? 'Start when you log in' : 'Start with Windows' })));
}

function routerCard(app) {
  const r = router;
  const st = app.status;
  const mac = isMac(st);
  const pub = [];
  for (const g of st.games) for (const p of g.ports || []) if (p.public) pub.push({ g, p });
  let summary;
  if (!r) summary = h('p', { class: 'muted' }, 'Loading…');
  else if (!r.on) summary = callout('info', 'info', h('p', null, 'The app doesn’t open router ports. Friends outside your home can only join games that give a code, unless you forward the ports below on your router yourself.'));
  else if (r.problem) summary = callout('warn', 'alert', h('p', null, r.problem));
  else if (r.cgnat || r.nat === 'cgnat') summary = callout('warn', 'alert', h('p', null, 'Your internet provider shares one internet address between homes (carrier-grade NAT), so friends outside can’t reach this computer by address. Games with a join code still work.'));
  else if (r.kind) summary = callout('good', 'check', h('p', null, `The router answers (${r.kind.toUpperCase()}), so the app opens each game’s ports while it runs.`), r.externalIP ? h('p', { class: 'small text-2' }, `Its internet address is ${r.externalIP}.`) : null);
  else summary = callout('warn', 'alert', h('p', null, 'The router didn’t answer the app. It may have UPnP or NAT-PMP turned off: forward the ports below on the router yourself.'));
  const maps = (r && r.mappings) || [];
  const stateOf = (g, p) => {
    const m = maps.filter((x) => x.instance === g.id && x.role === p.role);
    if (m.length) return m.map((x) => x.state).join(', ');
    return p.router || (isRunning(g) ? '' : 'when it runs');
  };
  const lan = (r && r.lan && r.lan[0]) || ((st.network || []).find((a) => a.connected && (a.ipv4 || []).length) || { ipv4: [] }).ipv4[0] || '';
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('router'), h('h2', null, 'Router ports'), r && r.checked ? h('span', { class: 'hint' }, `Checked ${ago(r.checked)}`) : null),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Open router ports for the games'),
        h('div', { class: 'help' }, 'While a game runs, the app asks the router (UPnP, NAT-PMP or PCP) to let friends outside your home reach it, and closes the ports again when it stops. Games that give friends a code don’t need it.')),
      toggle({ checked: !!(r && r.on), 'data-action': 'app-router', 'aria-label': 'Open router ports for the games', disabled: !r || !!saving })),
    summary,
    pub.length ? h('div', { class: 'table-wrap', style: { marginTop: '12px' } }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Game'), h('th', null, 'Port'), h('th', null, 'Number'), h('th', null, 'Protocol'), h('th', null, 'Router'))),
      h('tbody', null, pub.map(({ g, p }) => {
        const s = stateOf(g, p);
        return h('tr', { 'data-key': `${g.id}:${p.role}` },
          h('td', null, h('span', { class: 'row', style: { gap: '8px', flexWrap: 'nowrap' } }, tile(g, 'xs'), g.short)),
          h('td', null, portLabel(g.game, p.role)),
          h('td', { class: 'mono' }, String(p.port)),
          h('td', null, (p.protos || []).join(' + ').toUpperCase()),
          h('td', null, routerOpen(s) ? h('span', { class: 'badge good' }, s === 'open' ? 'Open' : 'Forwarded by hand') : h('span', { class: 'small muted' }, s || '–')));
      })))) : null,
    pub.length ? h('details', { class: 'where', style: { marginTop: '12px' } },
      h('summary', null, 'Setting up the ports on the router yourself'),
      h('ol', { class: 'steps' },
        h('li', null, 'Open your router’s settings page (often ', h('span', { class: 'mono' }, (r && r.gateway) || '192.168.1.1'), ' in a browser) and sign in.'),
        h('li', null, 'Find ', h('b', null, 'Port forwarding'), ' (sometimes under NAT, Virtual servers or Games).'),
        h('li', null, `Forward each port in the table, with its protocol, to this computer${lan ? ` (${lan.split(':')[0]})` : ''}.`),
        h('li', null, 'Come back here and click ', h('b', null, 'Check again'), '. A port forwarded by hand counts as open.')),
      ((r && r.games) || []).filter((x) => x.manual).map((x) => h('p', { class: 'small text-2' }, h('b', null, (gameByID(st, x.instance) || { short: x.instance }).short + ': '), x.manual))) : null,
    h('div', { class: 'row', style: { marginTop: '12px' } },
      h('button', { class: 'btn btn-sm', 'data-action': 'app-router-check', disabled: !r || r.checking }, icon('refresh'), r && r.checking ? 'Checking…' : 'Check again'),
      h('button', { class: 'btn btn-sm', 'data-action': 'fix-firewall' }, icon('shieldCheck'), mac ? 'Allow the games through the macOS firewall' : 'Allow the games through Windows Firewall')));
}

const CHARTS = [
  { id: 'cpu', title: 'CPU', format: fmtPct, minMax: 5, series: [{ key: 'cpuSys', label: 'Whole computer', color: '#3987e5', fill: true }] },
  { id: 'mem', title: 'Memory', format: fmtBytes, unit: 1024 ** 3, axisFormat: (v) => +(v / 1024 ** 3).toFixed(2) + ' GB', series: [{ key: 'memSys', label: 'Whole computer', color: '#3987e5', fill: true }] },
  { id: 'gpu', title: 'Graphics (GPU)', format: fmtPct, minMax: 5, series: [{ key: 'gpu', label: 'Busiest graphics card', color: '#3987e5', fill: true }] },
  { id: 'net', title: 'Network', format: fmtRate, unit: 1024, minMax: 50 * 1024, series: [{ key: 'netUp', label: 'Upload', color: '#3987e5' }, { key: 'netDown', label: 'Download', color: '#d95926' }] },
];

function computerCard(app) {
  const m = (app.status && app.status.machine) || {};
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('cpu'), h('h2', null, 'This computer'), h('span', { class: 'hint' }, 'the last hour')),
    h('dl', { class: 'kv', style: { gridTemplateColumns: 'auto 1fr', marginBottom: '14px' } },
      h('dt', null, 'Name'), h('dd', null, m.hostname || '–'),
      h('dt', null, 'Processor'), h('dd', null, m.cpu ? `${m.cpu} (${m.threads} threads)` : '–'),
      h('dt', null, 'Memory'), h('dd', null, m.memTotal ? fmtBytes(m.memTotal) : '–'),
      h('dt', null, 'Graphics'), h('dd', null, (m.gpus || []).join(', ') || '–'),
      h('dt', null, 'System'), h('dd', null, m.os || '–')),
    stats ? h('div', { class: 'grid grid-2' }, CHARTS.map((def) => h('div', { class: 'chart-card' },
      h('div', { class: 'chart-head' }, h('h3', null, def.title)), h('div', { 'data-chart': def.id, 'data-keep': '' }))))
      : h('p', { class: 'muted' }, 'Loading the charts…'));
}

function folderCard(app) {
  const st = app.status;
  const running = st.games.filter(isRunning);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('folder'), h('h2', null, 'Folders and closing')),
    h('div', { class: 'stack', style: { gap: '12px' } },
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-sm', 'data-action': 'open-folder', 'data-which': 'data' }, icon('folder'), 'The app’s folder'),
        h('button', { class: 'btn btn-sm', 'data-action': 'open-folder', 'data-which': 'logs' }, icon('terminal'), 'Logs folder')),
      h('div', { class: 'row', style: { paddingTop: '12px', borderTop: '1px solid var(--line)' } },
        running.length ? h('button', { class: 'btn', 'data-action': 'app-stop-all' }, icon('stop'), 'Stop every game') : null,
        h('button', { class: 'btn', 'data-action': 'app-quit', 'data-stop': '0' }, icon('x'), running.length ? 'Close the app (games keep running)' : 'Close the app'),
        running.length ? h('button', { class: 'btn btn-danger', 'data-action': 'app-quit', 'data-stop': '1' }, icon('power'), 'Stop every game and close the app') : null),
      h('p', { class: 'small muted' }, `${APP_NAME} ${view.version}`)));
}

export default {
  title: 'App settings',
  render(app) {
    appRef = app;
    if (!view) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…'));
    return h('div', { class: 'stack' }, updatesCard(app), routerCard(app), computerCard(app), folderCard(app));
  },
  afterRender() {
    if (!stats) return;
    const now = Date.now();
    for (const def of CHARTS) {
      const slot = document.querySelector(`[data-chart="${def.id}"]`);
      if (!slot) continue;
      let c = charts[def.id];
      if (!c) c = charts[def.id] = new LineChart({ ...def, crosshair, height: 140 });
      if (c.el.parentNode !== slot) slot.replaceChildren(c.el);
      c.setData(stats.samples || [], now - 60 * 60000, now);
    }
  },
  mount(app) {
    appRef = app;
    crosshair = new Crosshair();
    load();
    loadStats();
    loadCatalog().then(redraw).catch(() => {});
    clearInterval(timer);
    timer = setInterval(() => { loadStats(); api('/api/router').then((r) => { router = r; redraw(); }).catch(() => {}); }, 15000);
    const A = app.actions;
    A['app-auto-update'] = (d, el) => save({ autoUpdate: el.checked }, el.checked ? 'The app updates itself.' : 'The app won’t update itself.');
    A['app-router'] = (d, el) => save({ routerPorts: el.checked }, el.checked ? 'The app opens router ports for running games.' : 'The app doesn’t open router ports now.');
    A['app-router-check'] = async () => {
      try { await api('/api/router/check', { method: 'POST', body: {} }); toast('Checking the router again…', 'info'); } catch (e) { toast(e.message, 'bad', 9000); }
      if (router) router.checking = true;
      redraw();
      setTimeout(load, 3000);
    };
    A['app-update'] = async (d) => {
      const install = d.install === '1';
      checking = !install;
      redraw();
      try {
        await api('/api/app/update', { method: 'POST', body: { install } });
        if (install) toast('Updating the app. The panel reconnects in a few seconds.', 'good', 8000);
        else {
          const st = await api('/api/status');
          toast(st.app && st.app.latest ? `Build ${st.app.latest.build} is ready.` : 'The app is up to date.', 'good');
        }
      } catch (e) {
        toast(e.message, 'bad', 8000);
      } finally {
        checking = false;
        app.poll();
      }
    };
    A['app-login'] = async (d, el) => {
      const mac = isMac(app.status);
      try {
        await api('/api/system/launch-on-login', { method: 'POST', body: { enabled: el.checked } });
        view.launchOnLogin = el.checked;
        toast(el.checked ? (mac ? 'The app will start when you log in.' : 'The app will start with Windows.')
          : (mac ? 'The app won’t start when you log in any more.' : 'The app won’t start with Windows any more.'), 'good');
      } catch (e) {
        el.checked = !el.checked;
        toast(e.message, 'bad');
      }
    };
    A['app-stop-all'] = async () => {
      const running = app.status.games.filter(isRunning);
      if (!await confirmDialog({ title: 'Stop every game?', message: `${running.map((g) => g.short).join(', ')}: each world is saved, then its server stops.`, confirm: 'Stop every game', kind: 'danger', icon: 'stop' })) return;
      try { await api('/api/power/stop-all', { method: 'POST', body: {} }); toast('Stopping every game…', 'info'); } catch (e) { toast(e.message, 'bad', 9000); }
      app.poll();
    };
    A['app-quit'] = async (d) => {
      const stop = d.stop === '1';
      const ok = await confirmDialog({
        title: stop ? 'Stop every game and close the app?' : 'Close the app?',
        message: stop ? 'Each world is saved, the games stop, and the app closes.' : 'The games keep running. Open the app again to see them.',
        confirm: 'Close', kind: stop ? 'danger' : 'primary',
      });
      if (!ok) return;
      if (stop) {
        // The app's own quit only knows how to stop the one game of older
        // versions, so every game is stopped here first, then the app closes.
        try {
          await api('/api/power/stop-all', { method: 'POST', body: {} });
        } catch (e) {
          toast(e.message, 'bad', 9000);
          return;
        }
        toast('Saving and stopping every game, then closing the app…', 'info', 15000);
        const until = Date.now() + 5 * 60e3;
        for (;;) {
          const st = await api('/api/status').catch(() => null);
          if (!st || !(st.games || []).some(isRunning) || Date.now() > until) break;
          await new Promise((r) => setTimeout(r, 1500));
        }
      }
      try { await api('/api/system/quit', { method: 'POST', body: { stopServer: stop } }); } catch { /* the app is closing */ }
      document.body.replaceChildren(h('div', { class: 'boot' }, h('img', { src: '/assets/logo.svg', width: 56, height: 56, alt: '' }),
        h('p', null, stop ? 'Saving the worlds and closing… You can close this window.' : 'The app has closed. You can close this window.')));
    };
  },
  unmount() {
    clearInterval(timer);
    for (const c of Object.values(charts)) c.destroy();
    charts = {};
  },
};
