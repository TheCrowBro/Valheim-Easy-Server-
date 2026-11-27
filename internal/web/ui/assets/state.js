// Small helpers every page of the panel uses to read a game's entry in the
// status (GET /api/status's "games", spec 4.9): what state to show, its
// letter tile, how friends join, and how to find the game the old panel's
// addresses mean. No game is named here (TestNoGameIDs): everything comes
// from the status.

import { h, ago } from './dom.js';

// A game "runs" from the moment it starts until it has stopped. Running
// games show under Active in the sidebar even without a star.
const RUNNING = new Set(['online', 'starting', 'stopping']);
export const isRunning = (g) => !!g && RUNNING.has(g.state);

// busyJob is the game's job while it is under way.
export const busyJob = (g) => (g && g.job && !g.job.finished ? g.job : null);

// gameState is the word and the dot colour for a game's state.
export function gameState(g) {
  if (!g) return { label: 'Connecting…', dot: '' };
  const job = busyJob(g);
  if (job && job.kind === 'install') return { label: g.installed ? 'Updating…' : 'Installing…', dot: 'warn' };
  if (job && job.kind === 'remove') return { label: 'Removing…', dot: 'warn' };
  switch (g.state) {
    case 'online': return { label: 'Online', dot: 'good' };
    case 'starting': return { label: 'Starting…', dot: 'warn' };
    case 'stopping': return { label: 'Stopping…', dot: 'warn' };
    case 'not_installed': return { label: 'Not installed', dot: '' };
  }
  if (g.foreign) return { label: 'Not run by this version', dot: '' };
  if (g.disabled) return { label: 'Turned off', dot: '' };
  if (crashed(g)) return { label: 'Crashed', dot: 'bad' };
  return { label: 'Offline', dot: '' };
}

// crashed reports whether the game stopped by itself in the last hour.
export function crashed(g) {
  const x = g && g.lastExit;
  return !!(x && !x.expected && Date.now() - new Date(x.at) < 3600e3 && !isRunning(g));
}

// canStart reports whether the panel offers Start for a game, and if not,
// why (for the button's tooltip).
export function startProblem(g) {
  if (g.foreign) return g.foreign;
  if (g.canRun === 'no') return g.needsText || 'This game’s server can’t run on this computer.';
  if (g.disabled) return 'This game is turned off. Turn it on again on its Settings tab.';
  if (busyJob(g)) return 'Please wait until ' + busyJob(g).title.toLowerCase() + ' has finished.';
  return '';
}

// tile is a game's letter tile in its colour (the panel uses no logos).
export function tile(g, size) {
  return h('span', { class: `gm${size ? ' ' + size : ''}`, style: { '--g': g.color || 'var(--muted)' }, 'aria-hidden': 'true' }, g.letter || '?');
}

// legacyID is the instance the old panel's addresses (#/home, #/worlds …)
// mean: the one game kept in the old files. The status doesn't mark it
// yet, so it is the game whose details the status's own top-level fields
// describe (they are always the legacy game's), else the first game.
export function legacyID(st) {
  const games = (st && st.games) || [];
  const marked = games.find((g) => g.legacy);
  if (marked) return marked.id;
  const same = games.find((g) => g.serverName === st.serverName && g.world === st.world && g.state === st.state);
  return same ? same.id : games.length ? games[0].id : '';
}

export function gameByID(st, id) {
  return ((st && st.games) || []).find((g) => g.id === id) || null;
}

// fmtMB writes a size given in megabytes the way people say it.
export function fmtMB(mb) {
  if (!mb || mb < 0) return '0 MB';
  if (mb >= 1024) return (mb / 1024).toFixed(mb >= 10240 ? 0 : 1).replace(/\.0$/, '') + ' GB';
  return Math.round(mb) + ' MB';
}

// formatCode spaces out a six-digit code ("393 279") so it is easy to read
// aloud; any other code is shown as it is.
export const formatCode = (code) => (/^\d{6}$/.test(code || '') ? code.replace(/(\d{3})(\d{3})/, '$1 $2') : code || '');

// joinFact is the one thing friends need to join, for a game's card: its
// label, the text shown and the text copied. null when there is nothing
// to give yet.
export function joinFact(g) {
  const j = g.join || {};
  if (j.how === 'code' || j.how === 'id') {
    if (!j.code) return null;
    return { label: j.codeLabel || (j.how === 'id' ? 'Game ID' : 'Join code'), text: formatCode(j.code), copy: j.code, what: 'code' };
  }
  const addr = j.address || (j.lan || [])[0];
  if (!addr) return null;
  return { label: j.address ? 'Address' : 'Address at home', text: addr, copy: addr, what: 'address' };
}

// playersText is "2 of 10 playing · Astrid, Bjorn".
export function playersText(g) {
  const names = (g.players || []).map((p) => p.name);
  const n = g.count >= 0 ? g.count : names.length;
  return `${n} of ${g.maxPlayers || '?'} playing` + (names.length ? ' · ' + names.join(', ') : '');
}

// lastPlayed says when the game last stopped, for a card of a stopped game.
export function lastRun(g) {
  return g.lastExit && g.lastExit.at ? `Stopped ${ago(g.lastExit.at)}` : '';
}

// countWord is "1 game" / "3 games".
export const countWord = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// listNames joins names as people write them: "A", "A and B", "A, B and C".
export function listNames(names) {
  if (names.length < 2) return names.join('');
  return names.slice(0, -1).join(', ') + ' and ' + names[names.length - 1];
}

// routerOpen reports whether a port's router state lets friends outside
// in: the app opened it, or the owner forwarded it by hand.
export const routerOpen = (state) => state === 'open' || state === 'set on the router';

// enc makes an instance id safe in a URL path.
export const enc = encodeURIComponent;
