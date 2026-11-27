// A game's Log tab: its server's own output, live, and a download of its
// current log. Each instance keeps its own lines and its own place in the
// log (the cursor), so switching games never mixes their lines up.

import { h, icon, api } from './dom.js';
import { moduleFor } from './games.js';
import { enc, gameByID } from './state.js';

const MAX_LINES = 3000;
const logs = new Map(); // instance -> { lines, last, filter, follow }
let timer = null;
let consoleEl = null;
let openFor = null;
let appRef = null;

function logFor(id) {
  let l = logs.get(id);
  if (!l) logs.set(id, l = { lines: [], last: 0, filter: '', follow: true });
  return l;
}

// lineClass styles a line: the game's module says which lines matter in
// its log; the shell marks errors either way.
function lineClass(id, text) {
  const g = appRef && gameByID(appRef.status, id);
  const own = g ? moduleFor(g.game).logClass(text) : null;
  if (own === 'join' || own === 'leave') return 'hl';
  if (own === 'save') return 'ok';
  if (own === 'warn') return 'err';
  if (/\b(error|exception|failed|crash(ed)?)\b/i.test(text)) return 'err';
  return '';
}

const timeFmt = new Intl.DateTimeFormat([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });

function lineEl(id, l) {
  if (l.skipped) return h('div', { class: 'skipped' }, l.text);
  return h('div', { class: lineClass(id, l.text) }, h('span', { class: 't' }, timeFmt.format(new Date(l.time))), l.text);
}

const matches = (lg, l) => !lg.filter || l.skipped || l.text.toLowerCase().includes(lg.filter);

function redraw(id) {
  if (!consoleEl || id !== openFor) return;
  const lg = logFor(id);
  const shown = lg.lines.filter((l) => matches(lg, l));
  consoleEl.replaceChildren(...(shown.length ? shown.map((l) => lineEl(id, l))
    : [h('div', { class: 'muted' }, lg.lines.length ? 'No lines match.' : 'Nothing logged yet. Start the server to see its output here.')]));
  if (lg.follow) consoleEl.scrollTop = consoleEl.scrollHeight;
}

async function fetchLines(id) {
  const lg = logFor(id);
  try {
    const res = await api(`/api/games/${enc(id)}/logs?after=${lg.last}`);
    if (res.last < lg.last) { // the app restarted: start over
      lg.lines = [];
      lg.last = 0;
      return fetchLines(id);
    }
    const got = res.lines || [];
    // The app sends at most the newest 1000 lines. If more came since the
    // last look (say the page was in a background tab), say that some are
    // missing rather than joining the two parts up silently.
    const missed = lg.last && got.length ? got[0].seq - lg.last - 1 : 0;
    if (missed > 0) got.unshift({ skipped: true, text: `… ${missed} line${missed === 1 ? '' : 's'} not shown here. Download the log to see everything. …` });
    lg.last = res.last;
    if (!got.length || id !== openFor) {
      lg.lines.push(...got);
      if (lg.lines.length > MAX_LINES) lg.lines = lg.lines.slice(-MAX_LINES);
      return;
    }
    const atBottom = consoleEl && consoleEl.scrollHeight - consoleEl.scrollTop - consoleEl.clientHeight < 40;
    lg.lines.push(...got);
    const overflow = lg.lines.length > MAX_LINES;
    if (overflow) lg.lines = lg.lines.slice(-MAX_LINES);
    if (!consoleEl) return;
    if (overflow || lg.filter) return redraw(id);
    if (consoleEl.firstChild && consoleEl.firstChild.classList && consoleEl.firstChild.classList.contains('muted')) consoleEl.replaceChildren();
    consoleEl.append(...got.map((l) => lineEl(id, l)));
    if (lg.follow && atBottom !== false) consoleEl.scrollTop = consoleEl.scrollHeight;
  } catch {
    /* shown by the connection banner */
  }
}

export default {
  render(app, g) {
    appRef = app;
    const lg = logFor(g.id);
    return h('div', { class: 'stack', style: { gap: '12px' } },
      h('div', { class: 'row' },
        h('input', { class: 'input', type: 'search', placeholder: 'Filter lines…', 'data-log-filter': g.id, value: lg.filter, style: { maxWidth: '320px' }, 'aria-label': 'Filter the log' }),
        h('label', { class: 'row', style: { gap: '8px', cursor: 'pointer' } },
          h('input', { type: 'checkbox', checked: lg.follow, 'data-log-follow': g.id }), 'Follow new lines'),
        h('span', { class: 'spacer' }),
        h('a', { class: 'btn btn-sm', href: `/api/games/${enc(g.id)}/logs/download`, download: '' }, icon('download'), 'Download log')),
      h('div', { class: 'console', role: 'log', 'aria-live': 'off', 'data-keep': '', id: 'console', 'data-key': 'console:' + g.id }));
  },
  afterRender(app, g) {
    const el = document.getElementById('console');
    if (el && el !== consoleEl) {
      consoleEl = el;
      redraw(g.id);
    }
  },
  mount(app, g) {
    appRef = app;
    openFor = g.id;
    consoleEl = null;
    fetchLines(g.id).then(() => { consoleEl = null; app.render(); });
    clearInterval(timer);
    timer = setInterval(() => { if (openFor) fetchLines(openFor); }, 1500);
    document.addEventListener('input', onInput);
  },
  unmount() {
    clearInterval(timer);
    document.removeEventListener('input', onInput);
    consoleEl = null;
    openFor = null;
  },
};

function onInput(e) {
  const t = e.target;
  if (t.hasAttribute('data-log-filter')) {
    const id = t.getAttribute('data-log-filter');
    logFor(id).filter = t.value.trim().toLowerCase();
    redraw(id);
  } else if (t.hasAttribute('data-log-follow')) {
    const id = t.getAttribute('data-log-follow');
    logFor(id).follow = t.checked;
    if (t.checked) redraw(id);
  }
}
