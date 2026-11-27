// A game's Stats tab (spec 6.1): its server's CPU, memory and players over
// time, and its usage by how many people were playing. The whole
// computer's charts are on the App settings page.

import { h, icon, api, toast, confirmDialog, fmtBytes, fmtPct, dur, clock, dateTime } from './dom.js';
import { LineChart, Crosshair } from './charts.js';
import { enc } from './state.js';

const S1 = '#3987e5'; // the game's server
const S2 = '#d95926'; // the whole computer

const RANGES = [['15m', '15 min', 15], ['1h', '1 hour', 60], ['6h', '6 hours', 360], ['24h', '24 hours', 1440]];
const running = (k) => (s) => (s.running ? s[k] : null);

function chartsFor(g) {
  return [
    { id: 'players', title: 'Players online', format: (v) => String(Math.round(v)), minMax: 2, integer: true, fill: true,
      series: [{ key: 'players', label: 'Players', color: S1, fill: true }] },
    { id: 'cpu', title: 'CPU', format: fmtPct, minMax: 5,
      series: [{ get: running('cpu'), label: `${g.short} server`, color: S1 }, { key: 'cpuSys', label: 'Whole computer', color: S2 }] },
    { id: 'mem', title: 'Memory', format: fmtBytes, unit: 1024 ** 3, axisFormat: (v) => +(v / 1024 ** 3).toFixed(2) + ' GB',
      series: [{ get: running('mem'), label: `${g.short} server`, color: S1 }, { key: 'memSys', label: 'Whole computer', color: S2 }] },
  ];
}

const views = new Map(); // instance -> { data, range, table }
let charts = {};
let chartsGame = '';
let crosshair = null;
let timer = null;
let appRef = null;
let openFor = null;

function viewFor(id) {
  let v = views.get(id);
  if (!v) views.set(id, v = { data: null, range: '1h', table: false });
  return v;
}

async function load(id) {
  const v = viewFor(id);
  try {
    v.data = await api(`/api/games/${enc(id)}/stats?range=${v.range}`);
  } catch (e) {
    if (!v.data) toast(e.message, 'bad');
    return;
  }
  if (appRef) appRef.render();
}

function schedule(id) {
  clearInterval(timer);
  const secs = { '15m': 5, '1h': 10, '6h': 30, '24h': 60 }[viewFor(id).range] || 10;
  timer = setInterval(() => { if (openFor) load(openFor); }, secs * 1000);
}

function latest(v, series) {
  const s = v.data && v.data.samples.length ? v.data.samples[v.data.samples.length - 1] : null;
  if (!s) return null;
  return series.get ? series.get(s) : s[series.key];
}

function chartCard(v, def) {
  const legend = def.series.length > 1 ? h('div', { class: 'legend' }, def.series.map((ser) => {
    const x = latest(v, ser);
    return h('span', null, h('i', { style: { background: ser.color } }), ser.label, x != null ? h('b', null, def.format(x)) : null);
  })) : null;
  return h('section', { class: 'card chart-card', 'aria-label': def.title },
    h('div', { class: 'chart-head' }, h('h3', null, def.title), legend),
    h('div', { 'data-chart': def.id, 'data-keep': '' }));
}

function summary(g, v) {
  const s = v.data.samples;
  const run = s.filter((x) => x.running);
  const avg = (arr, k) => (arr.length ? arr.reduce((n, x) => n + x[k], 0) / arr.length : null);
  const peakPlayers = s.reduce((m, x) => Math.max(m, x.players), 0);
  const peakMem = run.reduce((m, x) => Math.max(m, x.mem), 0);
  const tile = (label, value, sub) => h('div', { class: 'tile' }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), h('div', { class: 'sub' }, sub));
  return h('div', { class: 'tiles' },
    tile('Most players at once', String(peakPlayers), 'in this time'),
    tile('Average CPU', run.length ? fmtPct(avg(run, 'cpu')) : '–', 'while running'),
    tile('Most memory', run.length ? fmtBytes(peakMem) : '–', 'while running'),
    tile(`${g.short} running`, s.length && v.data.rangeSeconds ? Math.min(100, Math.round((v.data.runningSeconds / v.data.rangeSeconds) * 100)) + '%' : '–', 'of this time'));
}

function tableView(v) {
  const rows = v.data.samples.slice(-120).reverse();
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Readings'), h('span', { class: 'hint' }, `latest ${rows.length}`)),
    h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, ['Time', 'Players', 'Server CPU', 'Computer CPU', 'Server memory', 'Computer memory'].map((t, i) => h('th', { class: i ? 'num' : null }, t)))),
      h('tbody', null, rows.map((s) => h('tr', null,
        h('td', null, clock(s.t)), h('td', { class: 'num' }, s.players),
        h('td', { class: 'num' }, s.running ? fmtPct(s.cpu) : '–'), h('td', { class: 'num' }, fmtPct(s.cpuSys)),
        h('td', { class: 'num' }, s.running ? fmtBytes(s.mem) : '–'), h('td', { class: 'num' }, fmtBytes(s.memSys))))))));
}

function byPlayersCard(g, v) {
  const bp = v.data.byPlayers || { buckets: [] };
  const rows = (bp.buckets || []).filter((b) => b.seconds >= 30);
  const maxCPU = Math.max(1, ...rows.map((b) => b.cpu));
  const maxMem = Math.max(1, ...rows.map((b) => b.mem));
  const bar = (x, max, text) => h('div', { class: 'bar-cell' }, h('span', null, text), h('span', { class: 'bar', 'aria-hidden': 'true' }, h('i', { style: { width: Math.max(2, (x / max) * 100) + '%' } })));
  const est = bp.estimate;
  return h('section', { class: 'card', 'aria-label': 'Usage by number of players' },
    h('div', { class: 'card-head' }, h('h2', null, 'Usage by number of players'),
      bp.since ? h('span', { class: 'hint' }, `since ${dateTime(bp.since)}`) : null,
      h('button', { class: 'btn btn-ghost btn-sm', 'data-action': 'stats-reset', 'data-id': g.id }, icon('refresh'), 'Reset')),
    est ? h('div', { class: 'callout info', style: { marginBottom: '16px' } }, icon('users'), h('div', { class: 'body' },
      h('p', null, h('strong', null, `Each extra player adds about ${fmtPct(est.cpuPerPlayer)} CPU and ${fmtBytes(est.memPerPlayer)} of memory.`)),
      h('p', { class: 'small text-2' }, 'A rough estimate from what was measured here.')))
      : h('p', { class: 'small text-2', style: { marginBottom: '14px' } }, 'Play for a while with different numbers of people to see how much each extra player costs.'),
    rows.length ? h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Players'), h('th', { class: 'num' }, 'Time seen'), h('th', { class: 'num' }, 'Server CPU (avg)'),
        h('th', { class: 'num' }, 'Server memory (avg)'), h('th', { class: 'num' }, 'Computer CPU'))),
      h('tbody', null, rows.map((b) => h('tr', null,
        h('td', null, h('b', null, b.players === 0 ? 'Nobody' : `${b.players} player${b.players === 1 ? '' : 's'}`)),
        h('td', { class: 'num' }, dur(b.seconds)),
        h('td', { class: 'num' }, bar(b.cpu, maxCPU, fmtPct(b.cpu))),
        h('td', { class: 'num' }, bar(b.mem, maxMem, fmtBytes(b.mem))),
        h('td', { class: 'num' }, fmtPct(b.cpuSys)))))))
      : h('div', { class: 'empty' }, 'No data yet: this fills in while the server is running.'));
}

function destroyCharts() {
  for (const c of Object.values(charts)) c.destroy();
  charts = {};
}

export default {
  render(app, g) {
    appRef = app;
    const v = viewFor(g.id);
    const filters = h('div', { class: 'row', style: { marginBottom: '16px' } },
      h('div', { class: 'segmented', role: 'group', 'aria-label': 'Time range' },
        RANGES.map(([id, label]) => h('button', { class: v.range === id ? 'on' : '', 'data-action': 'stats-range', 'data-id': g.id, 'data-range': id, 'aria-pressed': String(v.range === id) }, label))),
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn btn-sm', 'data-action': 'stats-table', 'data-id': g.id, 'aria-pressed': String(v.table) }, icon(v.table ? 'chart' : 'table'), v.table ? 'Show charts' : 'Show as table'));
    if (!v.data) return h('div', null, filters, h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…')));
    return h('div', { class: 'stack' },
      h('div', null, filters, summary(g, v)),
      v.table ? tableView(v) : h('div', { class: 'grid grid-2' }, chartsFor(g).map((def) => chartCard(v, def))),
      byPlayersCard(g, v),
      h('p', { class: 'small muted' }, 'The whole computer’s charts are on ', h('a', { href: '#/app/settings' }, 'App settings'), '.'));
  },
  afterRender(app, g) {
    const v = viewFor(g.id);
    if (!v.data || v.table) return;
    if (chartsGame !== g.id) {
      destroyCharts();
      chartsGame = g.id;
    }
    const now = Date.now();
    const minutes = RANGES.find((r) => r[0] === v.range)[2];
    for (const def of chartsFor(g)) {
      const slot = document.querySelector(`[data-chart="${def.id}"]`);
      if (!slot) continue;
      let c = charts[def.id];
      if (!c) c = charts[def.id] = new LineChart({ ...def, crosshair });
      if (c.el.parentNode !== slot) slot.replaceChildren(c.el);
      c.setData(v.data.samples, now - minutes * 60000, now);
    }
  },
  mount(app, g) {
    appRef = app;
    openFor = g.id;
    crosshair = new Crosshair();
    destroyCharts();
    load(g.id);
    schedule(g.id);
    const A = app.actions;
    A['stats-range'] = (d) => {
      viewFor(d.id).range = d.range;
      schedule(d.id);
      load(d.id);
      app.render();
    };
    A['stats-table'] = (d) => { const v = viewFor(d.id); v.table = !v.table; app.render(); };
    A['stats-reset'] = async (d) => {
      if (!await confirmDialog({ title: 'Reset usage by players?', message: 'This clears the “usage by number of players” numbers and starts measuring again. The charts are not affected.', confirm: 'Reset' })) return;
      try { await api(`/api/games/${enc(d.id)}/stats/reset`, { method: 'POST', body: {} }); } catch (e) { toast(e.message, 'bad'); }
      load(d.id);
    };
  },
  unmount() {
    clearInterval(timer);
    openFor = null;
    destroyCharts();
    chartsGame = '';
  },
};
