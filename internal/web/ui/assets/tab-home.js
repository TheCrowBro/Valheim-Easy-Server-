// A game's Home tab (spec 6.1): its state with Start, Stop and Restart,
// How friends join, who's on, when it stops by itself or the "still
// playing?" question, its usage, its setup checklist and its activity.

import { h, icon, api, fmtPct, dur, ago, clock, initials } from './dom.js';
import { sparkline } from './charts.js';
import { moduleFor, nodes } from './games.js';
import { gameState, busyJob, crashed, isRunning, startProblem, formatCode, fmtMB, enc, routerOpen } from './state.js';
import { jobLine, failedJob, callout, isMac } from './parts.js';
import { feedCard } from './feed.js';

const sparks = new Map(); // instance -> { samples, at, loading }

function heroTitle(g) {
  const job = busyJob(g);
  if (job && job.kind === 'install') return g.installed ? 'Updating…' : 'Installing…';
  if (g.state === 'not_installed') return 'Not installed yet';
  if (g.state === 'stopping') return 'Saving & stopping…';
  const s = gameState(g);
  return s.label === 'Crashed' ? 'Stopped unexpectedly' : s.label;
}

function heroActions(g) {
  const job = busyJob(g);
  const id = g.id;
  const problem = startProblem(g);
  if (job && g.state === 'starting') return [h('button', { class: 'btn btn-lg', 'data-action': 'game-stop', 'data-id': id }, icon('stop'), 'Stop')];
  if (job) {
    return g.state === 'stopping' && Date.now() - new Date(job.started) > 30e3
      ? [h('button', { class: 'btn btn-danger', 'data-action': 'game-force-stop', 'data-id': id }, icon('power'), 'Force stop')]
      : [];
  }
  switch (g.state) {
    case 'not_installed':
      return [h('button', { class: 'btn btn-primary btn-lg', 'data-action': 'game-install', 'data-id': id, disabled: !!(g.canRun === 'no' || g.foreign), title: problem || null }, icon('download'), 'Install the server')];
    case 'stopped':
      return [h('button', { class: 'btn btn-primary btn-lg', 'data-action': 'game-start', 'data-id': id, disabled: !!problem, title: problem || null }, icon('play'), 'Start server')];
    case 'starting':
      return [h('button', { class: 'btn btn-lg', 'data-action': 'game-stop', 'data-id': id }, icon('stop'), 'Stop')];
    case 'online':
      return [
        h('button', { class: 'btn btn-lg', 'data-action': 'game-restart', 'data-id': id }, icon('restart'), 'Restart'),
        h('button', { class: 'btn btn-lg', 'data-action': 'game-stop', 'data-id': id }, icon('stop'), 'Stop server'),
      ];
  }
  return [h('button', { class: 'btn btn-danger', 'data-action': 'game-force-stop', 'data-id': id }, icon('power'), 'Force stop')];
}

function hero(app, g) {
  const state = gameState(g);
  const job = busyJob(g);
  const facts = [];
  const world = g.runningWorld || g.world;
  if (world) facts.push(h('span', null, icon('globe'), `World: ${world}`));
  if (g.startedAt && isRunning(g)) facts.push(h('span', null, icon('clock'), `Up ${dur((Date.now() - new Date(g.startedAt)) / 1000)}`));
  if (g.join && g.join.port) facts.push(h('span', null, icon('network'), `Port ${g.join.port}`));
  const b = g.build;
  if (b && (b.version || b.build) && b.version !== 'fake') facts.push(h('span', null, icon('info'), `Version ${b.version || b.build}`));
  else if (b && b.date && g.installed) facts.push(h('span', null, icon('refresh'), `Server updated ${ago(b.date)}`));
  if (g.stage === 'preview') facts.push(h('span', { class: 'badge accent', title: 'New in this version of the app: tell the owner if anything looks wrong' }, 'Preview'));

  const extra = [];
  if (job) extra.push(jobLine(job, g.id));
  const failed = failedJob(app, g);
  if (failed) extra.push(failed);
  if (g.needsRestart && g.state === 'online' && !job) {
    extra.push(h('div', { class: 'callout warn jobline' }, icon('refresh'), h('div', { class: 'body' },
      h('p', null, 'Some settings changed while the server was running. Restart it to use them.'),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'game-restart', 'data-id': g.id }, 'Restart now')))));
  }
  if (crashed(g) && !job) {
    extra.push(h('div', { class: 'callout bad jobline' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, `The server stopped unexpectedly at ${clock(g.lastExit.at)}${g.lastExit.code != null ? ` (exit code ${g.lastExit.code})` : ''}.`),
      h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: `#/game/${enc(g.id)}/log` }, icon('terminal'), 'See what happened')))));
  }
  return h('section', { class: 'card hero', 'aria-label': 'Server status' },
    h('div', null,
      h('div', { class: 'hero-state' }, h('span', { class: `dot ${state.dot}` }), h('h2', null, heroTitle(g))),
      h('div', { class: 'facts' }, facts)),
    h('div', { class: 'hero-actions' }, heroActions(g)),
    extra);
}

// notices are what stands in the way, and the clocks that stop the game.
function notices(g) {
  const out = [];
  if (g.foreign) out.push(callout('warn', 'alert', h('p', null, g.foreign)));
  if (g.canRun === 'no') {
    out.push(callout('warn', 'alert', h('p', null, h('strong', null, (g.needsText || 'This game can’t run on this computer') + '. '),
      'Its worlds and settings stay here, so it can be started on a computer that can run it.')));
  }
  if (g.disabled) {
    out.push(callout('info', 'power', h('p', null, h('strong', null, 'This game is turned off. '), 'It can’t be started, from here or Discord, until it’s turned on again.'),
      h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: `#/game/${enc(g.id)}/settings` }, 'Open settings'))));
  }
  const a = g.ask;
  if (a && a.asked) {
    out.push(h('div', { class: 'callout warn' }, icon('help'), h('div', { class: 'body' },
      h('p', null, h('strong', null, 'Is anyone still playing? '),
        `It has been on for a long time without the app being able to tell who’s playing. ${a.stopAt ? `It turns off at ${clock(a.stopAt)} unless someone answers.` : ''}`),
      a.canRead ? h('p', { class: 'small text-2' }, 'A message in its Discord channel counts as an answer.')
        : a.problem ? h('p', { class: 'small text-2' }, a.problem) : null),
      h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'keep-on', 'data-id': g.id }, 'Keep it on')));
  } else if (a && a.askAt && isRunning(g)) {
    out.push(h('p', { class: 'small muted' }, icon('clock'), ` At ${clock(a.askAt)} the app asks whether anyone is still playing.`));
  }
  if (g.autoStop && g.autoStop.at && isRunning(g)) {
    out.push(h('div', { class: 'callout info' }, icon('timer'), h('div', { class: 'body' },
      h('p', null, `Stops at ${clock(g.autoStop.at)} if nobody joins.`),
      h('p', { class: 'small text-2' }, `It stops by itself after ${g.autoStop.minutes} minutes with nobody on. Change that on its Settings tab.`))));
  }
  for (const w of g.warnings || []) out.push(callout('warn', 'alert', h('p', null, w)));
  return out;
}

function copyBtn(text, label, aria) {
  return h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'copy', 'data-text': text, 'data-label': label, 'aria-label': aria }, icon('copy'));
}

function joinCard(app, g) {
  const j = g.join || {};
  const online = g.state === 'online';
  const body = [];
  const rows = [];
  if (j.how === 'code' || j.how === 'id') {
    const label = j.codeLabel || (j.how === 'id' ? 'Game ID' : 'Join code');
    body.push(h('div', { class: 'row', style: { justifyContent: 'space-between', marginBottom: '14px' } },
      h('div', null, h('div', { class: 'muted small' }, label),
        j.code ? h('div', { class: 'join-code' }, formatCode(j.code))
          : h('div', { class: 'text-2', style: { padding: '8px 0' } }, j.problem ? `No ${label.toLowerCase()}` : online || g.state === 'starting' ? 'Waiting for it…' : 'Shown here when the server is online.')),
      j.code ? h('button', { class: 'btn', 'data-action': 'copy', 'data-text': j.code, 'data-label': `${label} copied` }, icon('copy'), 'Copy') : null));
  }
  const lan = (j.lan || []).filter((a) => !a.startsWith('100.'));
  const addrRow = (label, addr) => [h('dt', null, label), h('dd', { class: 'mono' }, addr), h('dd', null, copyBtn(addr, 'Address copied', 'Copy the address'))];
  if (j.address) rows.push(addrRow(j.how === 'address' ? 'Address' : 'Or address', j.address));
  if (lan.length && j.how === 'address') rows.push(addrRow(j.address ? 'Same network' : 'Address at home', lan[0]));
  if (j.password) {
    const shown = app.revealed.has(g.id);
    rows.push(h('dt', null, 'Password'),
      h('dd', null, h('span', { class: 'pw' }, shown ? j.password : '•'.repeat(Math.min(12, j.password.length)))),
      h('dd', { class: 'row', style: { gap: '4px' } },
        h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'reveal', 'data-id': g.id, 'aria-label': shown ? 'Hide the password' : 'Show the password' }, icon(shown ? 'eyeOff' : 'eye')),
        copyBtn(j.password, 'Password copied', 'Copy the password')));
  }
  if (rows.length) body.push(h('dl', { class: 'kv' }, rows));
  if (j.how === 'address' && !j.address && !lan.length) body.push(h('p', { class: 'text-2' }, online ? 'Working out the address…' : 'The address shows here when the server is online.'));
  if (j.problem) body.unshift(callout('warn', 'alert', h('p', { class: 'small' }, j.problem)));
  if ((j.steps || []).length) body.push(h('ol', { class: 'steps' }, j.steps.map((s) => h('li', null, s))));
  if (j.router) body.push(routerNote(g));
  const d = g.discord;
  if (d && online) {
    if (d.error) body.push(callout('warn', 'alert', h('p', { class: 'small' }, h('strong', null, 'Couldn’t post in Discord. '), d.error)));
    else body.push(h('p', { class: 'small muted discord-line' }, icon(d.posted ? 'check' : 'message'), d.posted ? `Posted in Discord at ${clock(d.posted)}.` : 'Posting in Discord…'));
  }
  if (online && (g.reminder || []).length) {
    const text = g.reminder.join(', ');
    body.unshift(h('div', { class: 'callout warn reminder' }, icon('alert'), h('div', { class: 'body small' },
      h('p', null, h('strong', null, 'Before your friends join: '), text[0].toUpperCase() + text.slice(1) + '.'),
      h('p', { class: 'text-2' }, 'Things changed since they last played. ', d ? 'The Discord post says this too.' : 'Let them know.'))));
  }
  body.push(...nodes(moduleFor(g.game).joinExtras(g, h)));
  return h('section', { class: 'card', 'aria-label': 'How friends join' },
    h('div', { class: 'card-head' }, h('h2', null, 'How friends join'),
      j.crossplay ? h('span', { class: 'badge' }, 'Crossplay on') : null),
    body);
}

// routerNote says whether friends outside the home can get in, for a game
// that needs router ports.
function routerNote(g) {
  const pub = (g.ports || []).filter((p) => p.public);
  const states = pub.map((p) => p.router || '');
  const list = pub.map((p) => `${p.port} (${(p.protos || []).join('/').toUpperCase()})`).join(', ');
  if (states.length && states.every(routerOpen)) {
    return h('p', { class: 'small muted discord-line' }, icon('check'), 'The router lets friends outside your home in.');
  }
  const text = states.includes('failed')
    ? 'The app couldn’t open the router ports. App settings has the steps to set them up by hand.'
    : states.includes('off') || !states.some(Boolean)
      ? `Friends outside your home need port${pub.length === 1 ? '' : 's'} ${list} open on your router to this computer.`
      : 'Checking whether friends outside your home can get in…';
  return callout('info', 'router', h('p', { class: 'small' }, text), g.join.manual ? h('p', { class: 'small text-2' }, g.join.manual) : null,
    h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: '#/app/settings' }, 'Router settings')));
}

function playersCard(g) {
  const list = g.players || [];
  let body;
  if (list.length) {
    body = h('div', { class: 'list' }, list.map((p) => h('div', { class: 'list-row', 'data-key': p.name },
      h('div', { class: 'avatar', 'aria-hidden': 'true' }, initials(p.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, p.name), h('div', { class: 'sub' }, `Playing for ${dur((Date.now() - new Date(p.since)) / 1000)}`)))));
  } else if (g.state === 'online' && g.count > 0) {
    body = h('div', { class: 'empty' }, icon('users'), `${g.count} playing. ${g.short} doesn’t say who.`);
  } else if (g.state === 'online' && g.presence === 'none') {
    body = h('div', { class: 'empty' }, icon('users'), `${g.short} doesn’t tell the app who’s playing.`);
  } else {
    body = h('div', { class: 'empty' }, icon('users'), g.state === 'online' ? 'Nobody’s playing right now.' : 'The server is offline.');
  }
  return h('section', { class: 'card', 'aria-label': 'Playing now' },
    h('div', { class: 'card-head' }, h('h2', null, 'Playing now'), h('span', { class: 'hint' }, `${Math.max(0, g.count || list.length)} of ${g.maxPlayers || '?'}`)),
    body);
}

function tile(id, ic, label, value, sub) {
  return h('div', { class: 'tile' },
    h('div', { class: 'label' }, icon(ic), label),
    h('div', { class: 'value' }, value),
    h('div', { class: 'sub' }, sub || ''),
    h('canvas', { 'data-spark': id, 'data-keep': '', 'aria-hidden': 'true' }));
}

function tiles(g) {
  const running = isRunning(g);
  return h('section', { class: 'tiles', 'aria-label': 'Usage' },
    tile('cpu', 'cpu', 'Server CPU', running ? fmtPct(g.cpu) : '–', 'of the whole computer'),
    tile('mem', 'memory', 'Server memory', running ? fmtMB(g.memMB) : '–', g.needMB ? `needs about ${fmtMB(g.needMB)}` : ''),
    tile('players', 'users', 'Players', running ? String(Math.max(0, g.count)) : '–', `at most ${g.maxPlayers || '?'}`));
}

// checklist is the game's setup: what it needs on this computer before
// friends can play, plus anything only this game needs (its module).
function checklist(app, g) {
  const st = app.status;
  const c = st.checklist || {};
  const mac = isMac(st);
  const items = [
    [g.installed, `Install the ${g.short} server`, g.installed ? 'Installed, with anything it needs to run.' : 'Downloaded by the app, with anything it needs to run.',
      !g.installed && h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'game-install', 'data-id': g.id, disabled: g.canRun === 'no' || !!busyJob(g) }, 'Install')],
    mac ? [c.firewall, 'Let friends through the macOS firewall', c.firewall ? 'The firewall is off, or lets the servers through.' : 'The firewall is on. macOS asks for your password.', !c.firewall && h('button', { class: 'btn btn-sm', 'data-action': 'fix-firewall' }, 'Allow')]
      : [c.firewall, 'Allow it through Windows Firewall', c.firewall ? 'Friends can connect.' : 'So friends can connect. Windows asks for permission.', !c.firewall && h('button', { class: 'btn btn-sm', 'data-action': 'fix-firewall' }, 'Allow')],
  ];
  if (g.join && g.join.router) {
    const pub = (g.ports || []).filter((p) => p.public);
    const open = pub.length > 0 && pub.every((p) => routerOpen(p.router));
    items.push([open, 'Router ports for friends outside your home', open ? 'Open, so friends anywhere can join.' : 'Not open yet: the app tries when the server starts.',
      !open && h('a', { class: 'btn btn-sm', href: '#/app/settings' }, 'Router')]);
  }
  const rows = items.map(([done, title, help, btn]) => h('div', { class: `check-item${done ? ' done' : ''}` },
    h('div', { class: 'check-mark' }, icon(done ? 'check' : 'more')),
    h('div', { class: 'grow' }, h('div', { class: 'title' }, title), h('div', { class: 'help' }, help)),
    btn || null));
  const own = nodes(moduleFor(g.game).checklist(g, h));
  const left = items.filter((i) => !i[0]).length;
  return h('section', { class: 'card', 'aria-label': 'Setup' },
    h('div', { class: 'card-head' }, h('h2', null, 'Setup on this computer'), h('span', { class: 'hint' }, left ? `${left} left` : 'All done')),
    rows, own);
}

function drawSparks(id) {
  const s = (sparks.get(id) || {}).samples || [];
  if (!s.length) return;
  const run = (k) => s.map((x) => (x.running ? x[k] : null));
  const series = { cpu: [run('cpu'), null], mem: [run('mem'), null], players: [s.map((x) => x.players), null] };
  for (const c of document.querySelectorAll('canvas[data-spark]')) {
    const [vals, max] = series[c.dataset.spark] || [[]];
    sparkline(c, vals, '#3987e5', max);
  }
}

export default {
  render(app, g) {
    return h('div', { class: 'stack' },
      hero(app, g),
      notices(g),
      h('div', { class: 'grid grid-2' }, joinCard(app, g), playersCard(g)),
      tiles(g),
      h('div', { class: 'grid grid-2' }, feedCard(app.status, g.id), checklist(app, g)));
  },
  afterRender(app, g) {
    const sp = sparks.get(g.id) || { samples: [], at: 0, loading: false };
    sparks.set(g.id, sp);
    if (Date.now() - sp.at > 10000 && !sp.loading) {
      sp.loading = true;
      api(`/api/games/${enc(g.id)}/stats?range=15m`)
        .then((d) => { Object.assign(sp, { samples: d.samples || [], at: Date.now(), loading: false }); drawSparks(g.id); })
        .catch(() => { sp.loading = false; sp.at = Date.now(); });
    }
    drawSparks(g.id);
  },
};
