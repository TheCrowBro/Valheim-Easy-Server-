// Log: the Valheim server's own output, live.

import { h, icon, api } from './dom.js';

const MAX_LINES = 3000;
let lines = [];
let last = 0;
let filter = '';
let follow = true;
let timer = null;
let consoleEl = null;
let appRef = null;

function lineClass(text) {
  if (/error|exception|failed|crash/i.test(text)) return 'err';
  if (/Got character ZDOID|join code|Opened (Steam|PlayFab) server|Closing socket/.test(text)) return 'hl';
  if (/World save|World saved/.test(text)) return 'ok';
  return '';
}

const timeFmt = new Intl.DateTimeFormat([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });

function lineEl(l) {
  return h('div', { class: lineClass(l.text) }, h('span', { class: 't' }, timeFmt.format(new Date(l.time))), l.text);
}

function matches(l) {
  return !filter || l.text.toLowerCase().includes(filter);
}

function redraw() {
  if (!consoleEl) return;
  const shown = lines.filter(matches);
  consoleEl.replaceChildren(...(shown.length ? shown.map(lineEl)
    : [h('div', { class: 'muted' }, lines.length ? 'No lines match.' : 'Nothing logged yet. Start the server to see its output here.')]));
  if (follow) consoleEl.scrollTop = consoleEl.scrollHeight;
}

async function fetchLines() {
  try {
    const res = await api('/api/logs?after=' + last);
    if (res.last < last) { // the app restarted: start over
      lines = [];
      last = 0;
      return fetchLines();
    }
    last = res.last;
    if (!res.lines.length) return;
    const atBottom = consoleEl && consoleEl.scrollHeight - consoleEl.scrollTop - consoleEl.clientHeight < 40;
    lines.push(...res.lines);
    const overflow = lines.length > MAX_LINES;
    if (overflow) lines = lines.slice(-MAX_LINES);
    if (!consoleEl) return;
    if (overflow || filter) return redraw();
    if (consoleEl.firstChild && consoleEl.firstChild.classList && consoleEl.firstChild.classList.contains('muted')) consoleEl.replaceChildren();
    consoleEl.append(...res.lines.map(lineEl));
    if (follow && atBottom !== false) consoleEl.scrollTop = consoleEl.scrollHeight;
  } catch {
    /* shown by the connection banner */
  }
}

export default {
  id: 'log',
  title: 'Log',
  heading: 'Server log',
  icon: 'terminal',
  render(app) {
    appRef = app;
    return h('div', { class: 'stack', style: { gap: '12px' } },
      h('div', { class: 'row' },
        h('input', { class: 'input', type: 'search', placeholder: 'Filter lines…', 'data-log-filter': '', value: filter, style: { maxWidth: '320px' }, 'aria-label': 'Filter log lines' }),
        h('label', { class: 'row', style: { gap: '8px', cursor: 'pointer' } },
          h('input', { type: 'checkbox', checked: follow, 'data-log-follow': '' }), 'Follow new lines'),
        h('span', { class: 'spacer' }),
        h('a', { class: 'btn btn-sm', href: '/api/logs/download', download: '' }, icon('download'), 'Download log')),
      h('div', { class: 'console', role: 'log', 'aria-live': 'off', 'data-keep': '', id: 'console' }));
  },
  afterRender() {
    const el = document.getElementById('console');
    if (el && el !== consoleEl) {
      consoleEl = el;
      redraw();
    }
  },
  mount(app) {
    appRef = app;
    consoleEl = null;
    fetchLines().then(() => appRef.render());
    timer = setInterval(fetchLines, 1500);
    document.addEventListener('input', onInput);
  },
  unmount() {
    clearInterval(timer);
    document.removeEventListener('input', onInput);
    consoleEl = null;
  },
};

function onInput(e) {
  if (e.target.hasAttribute('data-log-filter')) {
    filter = e.target.value.trim().toLowerCase();
    redraw();
  } else if (e.target.hasAttribute('data-log-follow')) {
    follow = e.target.checked;
    if (follow) redraw();
  }
}
