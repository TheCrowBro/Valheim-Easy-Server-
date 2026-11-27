// What the parts of Valheim's module share: the routes of one instance,
// state kept per instance, starting the server with the memory check, the
// module's own styles, and the full status of the legacy instance.
//
// Everything here is keyed by the instance (game.id), never kept in one
// variable, so a page never shows another instance's data (games/README.md,
// "Everything is per instance").

import { h, api, openModal } from '../../dom.js';

// gameApi is the path of one of an instance's own routes (spec 6.2), such
// as gameApi(game, '/worlds') for GET /api/games/valheim/worlds.
export function gameApi(game, path) {
  return '/api/games/' + encodeURIComponent(game.id) + path;
}

// perGame keeps one object per instance, made by init the first time.
export function perGame(init) {
  const all = new Map();
  return (game) => {
    let s = all.get(game.id);
    if (!s) {
      s = init();
      all.set(game.id, s);
    }
    return s;
  };
}

// busy says the instance has a job under way (a start, an import, a backup...).
export function busy(game) {
  return !!(game && game.job && !game.job.finished);
}

// serverRunning says the server is up or on its way up.
export function serverRunning(game) {
  return !!game && (game.state === 'online' || game.state === 'starting');
}

// The legacy instance's full status. Valheim is the one legacy game, and
// its first instance (whose id is the game's id) keeps the old top-level
// fields of GET /api/status: mods, modsCode, lastSave, discord, platform.
// The tab modules get the shell's app, so they read it from there. The
// draw hooks only get the game's own entry, so they use the copy kept
// here (from the last time a tab was drawn, or the app was asked), and
// only while it is of the same run: the same job, world and state as the
// game's entry, which is new on every refresh. Every start is a job, and
// the mods and their code are fixed when the server starts, so a copy of
// the same run has the right ones.
const seen = { status: null, asked: 0, asking: false };

export function isLegacy(game) {
  return !!game && game.id === game.game;
}

export function noteStatus(st) {
  if (st && Array.isArray(st.games)) seen.status = st;
}

export function legacyStatus(game, app) {
  if (app && app.status) noteStatus(app.status);
  if (!isLegacy(game)) return null;
  if (app && app.status) return app.status;
  const st = seen.status;
  const then = st && st.games.find((g) => g.id === game.id);
  const jobOf = (g) => (g.job ? g.job.id : 0);
  if (then && jobOf(then) === jobOf(game) && then.runningWorld === game.runningWorld && then.state === game.state) return st;
  // The copy is of another run, or there is none yet: ask the app once in
  // the background, so the next refresh draws from a copy of this run. That
  // is one request each time the game starts a job (a start, a backup) or
  // its state or world changes, and none once the game's own entry carries
  // what the hooks need (game.mods, see modsOf).
  if (!seen.asking && Date.now() - seen.asked > 3000) {
    seen.asking = true;
    seen.asked = Date.now();
    api('/api/status').then(noteStatus, () => {}).finally(() => { seen.asking = false; });
  }
  return null;
}

// freshStatus asks the app for its status now, for a button that needs
// what the game's own entry doesn't carry (the computer's system for the
// tutorial, the mods code for the mods help).
export async function freshStatus() {
  try {
    noteStatus(await api('/api/status'));
    seen.asked = Date.now();
  } catch { /* the shell shows that the app can't be reached */ }
  return seen.status;
}

// modsOf is what players need for the running world, and the mods code.
// The game's own entry carries them once the manager puts them there; until
// then the legacy instance's top-level status does.
export function modsOf(game, app) {
  if (game && Array.isArray(game.mods)) return { mods: game.mods, code: game.modsCode || '' };
  const st = legacyStatus(game, app);
  return { mods: (st && st.mods) || [], code: (st && st.modsCode) || '' };
}


// redrawer remembers the shell's app from the module's tabs, so work that
// finishes later (a load, an upload) can redraw the page. The shell has one
// app, so it is never forgotten: redrawing a page that isn't Valheim's
// only draws it again.
export function redrawer() {
  let app = null;
  return {
    set(a) { app = a; if (a && a.status) noteStatus(a.status); },
    get app() { return app; },
    render() { if (app) app.render(); },
    poll() { if (app && app.poll) app.poll(); },
  };
}

// styles adds the module's stylesheet once. Its rules are today's panel
// rules for the Valheim-only parts, each under the .vh class that the
// module's own content carries, so they keep looking as they do today
// whatever the shell's stylesheet becomes, and never touch the shell's.
let styled = false;
export function styles() {
  if (styled || typeof document === 'undefined' || !document.head) return;
  styled = true;
  document.head.append(h('link', { rel: 'stylesheet', href: new URL('./valheim.css', import.meta.url).href, 'data-game': 'valheim' }));
}

// scoped marks a dialog as the module's, so its styles apply inside it.
export function scoped(modal) {
  if (modal && modal.el && modal.el.classList) modal.el.classList.add('vh');
  return modal;
}

const gb = (mb) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.max(1, Math.round(mb))} MB`);

// startGame starts the instance through the new panel's route, which checks
// the computer's memory first (spec 4.3). When memory is short the app
// answers with action "memory": the owner then picks a game to stop, or
// starts anyway, and the start is sent again with that choice, as the
// shell's own Start does (spec 6.1, Memory warning). It returns whether
// the start was sent and taken.
export async function startGame(game, memory = '') {
  try {
    await api(gameApi(game, '/start'), { method: 'POST', body: { memory } });
    return true;
  } catch (e) {
    if (!(e.data && e.data.action === 'memory' && e.data.data)) throw e;
    return memoryChoice(game, e.message, e.data.data);
  }
}

function memoryChoice(game, message, short) {
  return new Promise((resolve, reject) => {
    let answered = false;
    const again = (memory) => async () => {
      answered = true;
      try {
        await api(gameApi(game, '/start'), { method: 'POST', body: { memory } });
        resolve(true);
      } catch (err) {
        reject(err);
      }
    };
    const others = (short.running || []).filter((r) => r.id !== game.id);
    scoped(openModal({
      title: 'Not enough memory?',
      body: [h('p', null, message)],
      actions: [
        { label: 'Cancel' },
        { label: 'Start anyway', onClick: again('anyway') },
        ...others.map((r) => ({ label: `Stop ${r.name} (${gb(r.memMB)}) and start`, kind: 'primary', icon: 'stop', onClick: again('stop:' + r.id) })),
      ],
      onClose: () => { if (!answered) resolve(false); },
    }));
  });
}

