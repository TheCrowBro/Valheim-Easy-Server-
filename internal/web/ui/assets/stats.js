// Stats: CPU, memory, GPU, network and players over time, plus usage
// broken down by how many people were playing.

import { h, icon, api, toast, confirmDialog, fmtBytes, fmtRate, fmtPct, dur, clock, dateTime } from './dom.js';
import { LineChart, Crosshair } from './charts.js';

const S1 = '#3987e5'; // series 1: the Valheim server / main measure
const S2 = '#d95926'; // series 2: whole computer / second measure

const RANGES = [['15m', '15 min', 15], ['1h', '1 hour', 60], ['6h', '6 hours', 360], ['24h', '24 hours', 1440]];

const running = (k) => (s) => (s.running ? s[k] : null);
const CHARTS = [
  { id: 'players', title: 'Players online', format: (v) => String(Math.round(v)), minMax: 2, integer: true, fill: true,
    series: [{ key: 'players', label: 'Players', color: S1, fill: true }] },
  { id: 'cpu', title: 'CPU', format: fmtPct, minMax: 5,
    series: [{ get: running('cpu'), label: 'Valheim server', color: S1 }, { key: 'cpuSys', label: 'Whole computer', color: S2 }] },
  { id: 'mem', title: 'Memory', format: fmtBytes, unit: 1024 ** 3, axisFormat: (v) => +(v / 1024 ** 3).toFixed(2) + ' GB',
    series: [{ get: running('mem'), label: 'Valheim server', color: S1 }, { key: 'memSys', label: 'Whole computer', color: S2 }] },
  { id: 'gpu', title: 'Graphics (GPU)', format: fmtPct, minMax: 5,
    note: 'The Valheim server runs without graphics, so this mostly shows other programs.',
    series: [{ key: 'gpu', label: 'Busiest graphics card', color: S1, fill: true }] },
  { id: 'net', title: 'Network', format: fmtRate, unit: 1024, minMax: 50 * 1024,
    series: [{ key: 'netUp', label: 'Upload', color: S1 }, { key: 'netDown', label: 'Download', color: S2 }] },
];

let range = localStorage.getItem('ves-range') || '1h';
let showTable = false;
let data = null;
let charts = {};
let crosshair = null;
let timer = null;
let appRef = null;

async function load() {
  try {
    data = await api('/api/stats?range=' + range);
  } catch (e) {
    if (!data) toast(e.message, 'bad');
    return;
  }
  if (appRef && appRef.page.id === 'stats') appRef.render();
}

function schedule() {
  clearInterval(timer);
  const secs = { '15m': 5, '1h': 10, '6h': 30, '24h': 60 }[range] || 10;
  timer = setInterval(load, secs * 1000);
}

function latest(series) {
  const s = data && data.samples.length ? data.samples[data.samples.length - 1] : null;
  if (!s) return null;
  return series.get ? series.get(s) : s[series.key];
}

function chartCard(def) {
  const legend = def.series.length > 1 ? h('div', { class: 'legend' }, def.series.map((ser) => {
    const v = latest(ser);
    return h('span', null, h('i', { style: { background: ser.color } }), ser.label, v != null ? h('b', null, def.format(v)) : null);
  })) : null;
  return h('section', { class: 'card chart-card', 'aria-label': def.title },
    h('div', { class: 'chart-head' }, h('h3', null, def.title), def.note ? h('span', { class: 'small muted' }, def.note) : null, legend),
    h('div', { 'data-chart': def.id, 'data-keep': '' }));
}

function summary() {
  const s = data.samples;
  const run = s.filter((x) => x.running);
  const avg = (arr, k) => (arr.length ? arr.reduce((n, x) => n + x[k], 0) / arr.length : null);
  const peakPlayers = s.reduce((m, x) => Math.max(m, x.players), 0);
  const peakMem = run.reduce((m, x) => Math.max(m, x.mem), 0);
  const tile = (label, value, sub) => h('div', { class: 'tile' }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), h('div', { class: 'sub' }, sub));
  return h('div', { class: 'tiles' },
    tile('Most players at once', String(peakPlayers), 'in this time range'),
    tile('Average server CPU', run.length ? fmtPct(avg(run, 'cpu')) : '–', 'while running'),
    tile('Highest server memory', run.length ? fmtBytes(peakMem) : '–', 'while running'),
    tile('Average computer CPU', s.length ? fmtPct(avg(s, 'cpuSys')) : '–', 'whole computer'),
    tile('Server running', s.length ? Math.round((run.length / s.length) * 100) + '%' : '–', 'of this time range'));
}

function tableView() {
  const rows = data.samples.slice(-120).reverse();
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Readings'), h('span', { class: 'hint' }, `latest ${rows.length}`)),
    h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, ['Time', 'Players', 'Server CPU', 'Computer CPU', 'Server memory', 'Computer memory', 'GPU', 'Upload', 'Download']
        .map((t, i) => h('th', { class: i ? 'num' : null }, t)))),
      h('tbody', null, rows.map((s) => h('tr', null,
        h('td', null, clock(s.t)), h('td', { class: 'num' }, s.players),
        h('td', { class: 'num' }, s.running ? fmtPct(s.cpu) : '–'), h('td', { class: 'num' }, fmtPct(s.cpuSys)),
        h('td', { class: 'num' }, s.running ? fmtBytes(s.mem) : '–'), h('td', { class: 'num' }, fmtBytes(s.memSys)),
        h('td', { class: 'num' }, fmtPct(s.gpu)), h('td', { class: 'num' }, fmtRate(s.netUp)), h('td', { class: 'num' }, fmtRate(s.netDown))))))));
}

function byPlayersCard() {
  const bp = data.byPlayers;
  const rows = bp.buckets.filter((b) => b.seconds >= 30);
  const maxCPU = Math.max(1, ...rows.map((b) => b.cpu));
  const maxMem = Math.max(1, ...rows.map((b) => b.mem));
  const bar = (v, max, text) => h('div', { class: 'bar-cell' }, h('span', null, text), h('span', { class: 'bar', 'aria-hidden': 'true' }, h('i', { style: { width: Math.max(2, (v / max) * 100) + '%' } })));
  const est = bp.estimate;
  return h('section', { class: 'card', 'aria-label': 'Usage by number of players' },
    h('div', { class: 'card-head' }, h('h2', null, 'Usage by number of players'),
      h('span', { class: 'hint' }, `since ${dateTime(bp.since)}`),
      h('button', { class: 'btn btn-ghost btn-sm', 'data-action': 'stats-reset' }, icon('refresh'), 'Reset')),
    est ? h('div', { class: 'callout info', style: { marginBottom: '16px' } }, icon('users'),
      h('div', { class: 'body' },
        h('p', null, h('strong', null, `Each extra player adds about ${fmtPct(est.cpuPerPlayer)} CPU and ${fmtBytes(est.memPerPlayer)} of memory. `),
          `With 10 players (Valheim’s maximum), expect the server to use about ${fmtPct(est.cpuAt10)} CPU and ${fmtBytes(est.memAt10)} of memory.`),
        h('p', { class: 'small text-2' }, 'A rough estimate from what was measured here. Memory also grows as more of the world gets explored.'))) :
      h('p', { class: 'small text-2', style: { marginBottom: '14px' } }, 'Play for a while with different numbers of people to see how much each extra player costs.'),
    rows.length ? h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Players'), h('th', { class: 'num' }, 'Time seen'), h('th', { class: 'num' }, 'Server CPU (avg)'),
        h('th', { class: 'num' }, 'Server memory (avg)'), h('th', { class: 'num' }, 'Computer CPU'), h('th', { class: 'num' }, 'GPU'), h('th', { class: 'num' }, 'Network'))),
      h('tbody', null, rows.map((b) => h('tr', null,
        h('td', null, h('b', null, b.players === 0 ? 'Nobody' : `${b.players} player${b.players === 1 ? '' : 's'}`)),
        h('td', { class: 'num' }, dur(b.seconds)),
        h('td', { class: 'num' }, bar(b.cpu, maxCPU, fmtPct(b.cpu))),
        h('td', { class: 'num' }, bar(b.mem, maxMem, fmtBytes(b.mem))),
        h('td', { class: 'num' }, fmtPct(b.cpuSys)),
        h('td', { class: 'num' }, fmtPct(b.gpu)),
        h('td', { class: 'num' }, fmtRate(b.net)))))))
      : h('div', { class: 'empty' }, 'No data yet: this fills in while the server is running.'));
}

function machineCard() {
  const m = data.machine || {};
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Server computer')),
    h('dl', { class: 'kv', style: { gridTemplateColumns: 'auto 1fr' } },
      h('dt', null, 'Name'), h('dd', null, m.hostname || '–'),
      h('dt', null, 'Processor'), h('dd', null, m.cpu ? `${m.cpu} (${m.threads} threads)` : '–'),
      h('dt', null, 'Memory'), h('dd', null, m.memTotal ? fmtBytes(m.memTotal) : '–'),
      h('dt', null, 'Graphics'), h('dd', null, (m.gpus || []).join(', ') || '–'),
      h('dt', null, 'System'), h('dd', null, m.os || '–')));
}

export default {
  id: 'stats',
  title: 'Stats',
  icon: 'chart',
  render(app) {
    appRef = app;
    const filters = h('div', { class: 'row', style: { marginBottom: '16px' } },
      h('div', { class: 'segmented', role: 'group', 'aria-label': 'Time range' },
        RANGES.map(([id, label]) => h('button', { class: range === id ? 'on' : '', 'data-action': 'stats-range', 'data-range': id, 'aria-pressed': range === id ? 'true' : 'false' }, label))),
      h('span', { class: 'spacer' }),
      h('button', { class: 'btn btn-sm', 'data-action': 'stats-table', 'aria-pressed': showTable ? 'true' : 'false' }, icon(showTable ? 'chart' : 'table'), showTable ? 'Show charts' : 'Show as table'));
    if (!data) return h('div', null, filters, h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…')));
    return h('div', { class: 'stack' },
      h('div', null, filters, summary()),
      showTable ? tableView() : h('div', { class: 'grid grid-2' }, CHARTS.map(chartCard)),
      byPlayersCard(),
      machineCard());
  },
  afterRender() {
    if (!data || showTable) return;
    const now = Date.now();
    const minutes = RANGES.find((r) => r[0] === range)[2];
    for (const def of CHARTS) {
      const slot = document.querySelector(`[data-chart="${def.id}"]`);
      if (!slot) continue;
      let c = charts[def.id];
      if (!c) c = charts[def.id] = new LineChart({ ...def, crosshair });
      if (c.el.parentNode !== slot) slot.replaceChildren(c.el);
      c.setData(data.samples, now - minutes * 60000, now);
    }
  },
  mount(app) {
    appRef = app;
    crosshair = new Crosshair();
    load();
    schedule();
    const A = app.actions;
    A['stats-range'] = (d) => {
      range = d.range;
      localStorage.setItem('ves-range', range);
      schedule();
      load();
      app.render();
    };
    A['stats-table'] = () => { showTable = !showTable; app.render(); };
    A['stats-reset'] = async () => {
      if (!await confirmDialog({ title: 'Reset usage by players?', message: 'This clears the “usage by number of players” numbers and starts measuring again. The charts are not affected.', confirm: 'Reset' })) return;
      try { await api('/api/stats/reset', { method: 'POST', body: {} }); } catch (e) { toast(e.message, 'bad'); }
      load();
    };
  },
  unmount() {
    clearInterval(timer);
    for (const c of Object.values(charts)) c.destroy();
    charts = {};
  },
};
