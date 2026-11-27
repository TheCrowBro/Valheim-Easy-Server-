// Overview (spec 6.1): every game at a glance. The starred games come
// first as cards, then any game that runs without a star, then the other
// games in a folding list, each with Start. Below them, how the computer's
// memory is shared out, and what has happened in every game.

import { h, icon, fmtBytes, clock } from './dom.js';
import { lists, starButton } from './app.js';
import { gameState, isRunning, busyJob, tile, joinFact, playersText, lastRun, fmtMB, startProblem, enc } from './state.js';
import { feedCard } from './feed.js';
import { callout } from './parts.js';

let inactiveOpen = false; // the Overview's own folding list; closed when the page opens

// actionsFor are a game's buttons on its card or row.
function actionsFor(g, compact) {
  const job = busyJob(g);
  const out = [];
  const problem = startProblem(g);
  if (g.state === 'not_installed' && !job) {
    out.push(h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'game-install', 'data-id': g.id, disabled: !!problem, title: problem || null }, icon('download'), 'Install'));
  } else if (g.state === 'online' || g.state === 'starting') {
    out.push(h('button', { class: 'btn btn-sm', 'data-action': 'game-stop', 'data-id': g.id }, icon('stop'), 'Stop'));
  } else if (g.state === 'stopped') {
    out.push(h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'game-start', 'data-id': g.id, disabled: !!problem, title: problem || null }, icon('play'), 'Start'));
  }
  const fact = isRunning(g) ? joinFact(g) : null;
  if (fact && !compact) {
    out.push(h('button', { class: 'btn btn-sm', 'data-action': 'copy', 'data-text': fact.copy, 'data-label': `${g.short}: ${fact.label.toLowerCase()} copied` },
      icon('copy'), fact.what === 'code' ? 'Copy code' : 'Copy address'));
  }
  return out;
}

// passwordLine shows the password hidden until the eye is clicked, and
// copies it, as How friends join does.
function passwordLine(app, g) {
  const shown = app.revealed.has(g.id);
  const pw = g.join.password;
  return h('div', { class: 'kv-line row', style: { gap: '4px' } }, 'Password ',
    h('b', { class: 'mono pw' }, shown ? pw : '•'.repeat(Math.min(10, pw.length))),
    h('button', { class: 'btn btn-ghost btn-icon btn-xs', 'data-action': 'reveal', 'data-id': g.id, 'aria-label': shown ? 'Hide the password' : 'Show the password' }, icon(shown ? 'eyeOff' : 'eye')),
    h('button', { class: 'btn btn-ghost btn-icon btn-xs', 'data-action': 'copy', 'data-text': pw, 'data-label': `${g.short}: password copied`, 'aria-label': 'Copy the password' }, icon('copy')));
}

function gameCard(app, g) {
  const state = gameState(g);
  const job = busyJob(g);
  const running = isRunning(g);
  const fact = running ? joinFact(g) : null;
  const lines = [];
  if (running) {
    if (fact) lines.push(h('div', { class: 'kv-line' }, fact.label, ' ', h('b', { class: 'mono' }, fact.text)));
    else if (g.state === 'online' && g.join && g.join.problem) lines.push(h('div', { class: 'kv-line warn-text' }, g.join.problem));
    else lines.push(h('div', { class: 'kv-line' }, g.state === 'online' ? 'Waiting for how to join…' : 'Shown when it’s online'));
    if (g.join && g.join.password) lines.push(passwordLine(app, g));
    lines.push(h('div', { class: 'kv-line' }, playersText(g)));
  } else {
    lines.push(h('div', { class: 'kv-line' }, 'World ', h('b', null, g.world || '–')));
    const last = lastRun(g);
    lines.push(h('div', { class: 'kv-line' }, g.canRun === 'no' ? h('span', { class: 'warn-text' }, g.needsText || 'Can’t run on this computer')
      : last || `Needs about ${fmtMB(g.needMB)} of memory`));
  }
  if (g.autoStop && g.autoStop.at) lines.push(h('div', { class: 'kv-line small' }, icon('timer'), ` Stops at ${clock(g.autoStop.at)} if nobody joins`));
  const asked = g.ask && g.ask.asked;
  return h('article', { class: 'card gcard', style: { '--g': g.color }, 'data-key': 'card:' + g.id },
    h('div', { class: 'gcard-head' },
      tile(g),
      h('a', { class: 'gcard-name', href: `#/game/${enc(g.id)}/home` }, g.short),
      g.world ? h('span', { class: 'badge', title: 'World' }, g.runningWorld || g.world) : null,
      starButton(g)),
    h('div', { class: 'gcard-state' }, h('span', { class: `dot ${state.dot}` }), job ? job.title + '…' : state.label),
    lines,
    asked ? callout('warn', 'help', h('p', { class: 'small' }, h('strong', null, 'Is anyone still playing? '),
      g.ask.stopAt ? `It turns off at ${clock(g.ask.stopAt)} unless someone answers.` : 'It turns off soon unless someone answers.'),
    h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'keep-on', 'data-id': g.id }, 'Keep it on'))) : null,
    h('div', { class: 'gcard-actions' }, actionsFor(g),
      h('a', { class: 'btn btn-sm btn-ghost', href: `#/game/${enc(g.id)}/home` }, 'Open', icon('chevronRight'))));
}

function inactiveRow(g) {
  const state = gameState(g);
  const sub = [g.world ? `World ${g.world}` : '', g.canRun === 'no' ? g.needsText || 'Can’t run here' : state.label === 'Offline' ? lastRun(g) : state.label]
    .filter(Boolean).join(' · ');
  return h('div', { class: 'idle-row', 'data-key': 'row:' + g.id },
    tile(g),
    h('div', { class: 'grow' }, h('a', { class: 'name', href: `#/game/${enc(g.id)}/home` }, g.short), h('div', { class: 'sub' }, sub)),
    actionsFor(g, true),
    starButton(g));
}

// memoryCard is the memory bar: each running game in its colour, the rest
// of the computer's programs, and what's free.
function memoryCard(st) {
  const L = st.live || {};
  const total = L.memTotal || (st.machine && st.machine.memTotal) || 0;
  if (!total) return null;
  const used = Math.min(total, L.memSys || 0);
  const running = st.games.filter((g) => isRunning(g) && g.memMB > 0);
  const games = running.map((g) => ({ g, bytes: g.memMB * 1024 * 1024 }));
  const gameBytes = games.reduce((n, x) => n + x.bytes, 0);
  const other = Math.max(0, used - gameBytes);
  const free = Math.max(0, total - Math.max(used, gameBytes));
  const pct = (b) => `${Math.max(0, Math.min(100, (b / total) * 100)).toFixed(2)}%`;
  return h('section', { class: 'card mem', 'aria-label': 'Memory on this computer' },
    h('div', { class: 'card-head' }, icon('memory'), h('h2', null, 'Memory on this computer'), h('span', { class: 'badge' }, fmtBytes(total))),
    h('div', { class: 'membar', role: 'img', 'aria-label': `${fmtBytes(free)} of ${fmtBytes(total)} free` },
      games.map((x) => h('i', { style: { '--g': x.g.color, width: pct(x.bytes) }, title: `${x.g.short}: ${fmtBytes(x.bytes)}` })),
      other ? h('i', { class: 'other', style: { width: pct(other) }, title: `Everything else: ${fmtBytes(other)}` }) : null),
    h('div', { class: 'mem-legend' },
      games.map((x) => h('span', { style: { '--g': x.g.color } }, `${x.g.short} ${fmtBytes(x.bytes)}`)),
      h('span', { class: 'other' }, `Everything else ${fmtBytes(other)}`),
      h('span', { class: 'free' }, `${fmtBytes(free)} free`)));
}

function idleBanner(st) {
  if (!st.idle || st.playing) return null;
  const mins = Math.max(0, Math.round((new Date(st.idle.hibernateAt) - Date.now()) / 60000));
  const sleep = st.restMode === 'sleep' || !st.canHibernate;
  return h('div', { class: 'callout warn' }, icon('moon'),
    h('div', { class: 'body' }, h('p', null, h('strong', null, 'Nobody is playing. '),
      sleep ? (mins > 0 ? `This computer goes to sleep by itself in ${mins} min.` : 'This computer is about to go to sleep.')
        : (mins > 0 ? `This computer hibernates by itself in ${mins} min.` : 'This computer is about to hibernate.'))),
    h('button', { class: 'btn btn-sm', 'data-action': 'keep-awake' }, icon('sun'), 'Keep it awake'));
}

export default {
  title: 'Overview',
  render(app) {
    const st = app.status;
    const { starred, running, inactive } = lists(st);
    const cards = [...starred, ...running];
    return h('div', { class: 'stack' },
      (st.warnings || []).map((w) => callout('warn', 'alert', h('p', null, w))),
      idleBanner(st),
      !st.games.length
        ? h('section', { class: 'card empty-state' }, h('h2', null, 'No games yet'),
          h('p', { class: 'text-2' }, 'Add the first game your friends want to play. The app downloads its server and everything it needs.'),
          h('a', { class: 'btn btn-primary', href: '#/add' }, icon('plus'), 'Add a game'))
        : cards.length
          ? h('div', { class: 'gcards' }, cards.map((g) => gameCard(app, g)))
          : h('div', { class: 'card muted' }, 'Star the games you’re playing to pin them here. A game also shows here while it runs.'),
      inactive.length ? h('section', { class: 'card ov-inactive' },
        h('button', { class: 'twirl ov-twirl', type: 'button', 'data-action': 'ov-twirl', 'aria-expanded': String(inactiveOpen), 'aria-controls': 'ov-inactive' },
          icon('chevronDown'), 'Inactive games', h('span', { class: 'n' }, `(${inactive.length})`)),
        inactiveOpen ? h('div', { id: 'ov-inactive', class: 'idle-list' }, inactive.map(inactiveRow)) : null) : null,
      memoryCard(st),
      feedCard(st));
  },
  mount(app) {
    app.actions['ov-twirl'] = () => { inactiveOpen = !inactiveOpen; app.render(); };
  },
};
