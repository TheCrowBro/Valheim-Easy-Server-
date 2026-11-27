// The games' own modules (assets/games/<id>.js, the contract in
// games/README.md). The shell loads one with import() for each game it
// hears of, so adding a game never means editing a list here. A game
// without a module, or whose module fails to load, gets the defaults:
// every hook answers nothing and its pages show what the status and its
// settings give.

import { toast } from './dom.js';

// The defaults, which are also the shape every module is merged into.
const DEFAULTS = {
  joinExtras: () => null,
  logClass: () => null,
  feedIcons: {},
  worldsHelp: '',
  uploadAccept: '.zip',
  worldCard: () => null,
  tabs: {},
  checklist: () => null,
};

const loaded = new Map(); // game id -> the merged module, once loaded
const loading = new Map(); // game id -> its promise
let onLoaded = () => {};

// whenLoaded sets what to do once a module arrives (the shell redraws).
export function whenLoaded(fn) { onLoaded = fn; }

// A game id is a short lower-case name. Anything else is never asked for,
// so a strange id from the app can't point the import elsewhere.
const okID = (id) => typeof id === 'string' && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(id);

// moduleFor is a game's module, or the defaults while it loads (or when it
// has none).
export function moduleFor(gameID) {
  const m = loaded.get(gameID);
  if (m) return m;
  load(gameID);
  return DEFAULTS;
}

// isLoaded reports whether the game's module has been looked for already
// (found or not), so a page can tell "no Mods tab" from "not loaded yet".
export const isLoaded = (gameID) => loaded.has(gameID);

// load loads the modules of the given games, once each.
export function load(...ids) {
  for (const id of ids.flat()) {
    if (!okID(id) || loaded.has(id) || loading.has(id)) continue;
    const p = import(`./games/${id}.js`)
      .then((mod) => {
        const m = mod && mod.default;
        if (!m || typeof m !== 'object' || m.id !== id) return DEFAULTS; // not this game's module
        return merge(m);
      })
      .catch(() => DEFAULTS) // no module for this game: it works without one
      .then((m) => {
        loaded.set(id, m);
        loading.delete(id);
        onLoaded(id);
      });
    loading.set(id, p);
  }
}

// merge puts a module's hooks over the defaults. A hook that throws is
// reported once and then treated as answering nothing, so one game's
// mistake never breaks the whole page.
function merge(m) {
  const out = { ...DEFAULTS, ...m, feedIcons: { ...(m.feedIcons || {}) }, tabs: { ...(m.tabs || {}) } };
  for (const k of ['joinExtras', 'logClass', 'worldCard', 'checklist']) {
    const fn = typeof m[k] === 'function' ? m[k] : DEFAULTS[k];
    let warned = false;
    out[k] = (...args) => {
      try {
        return fn.apply(m, args);
      } catch (e) {
        if (!warned) {
          warned = true;
          console.error(`games/${m.id}.js ${k}:`, e);
          toast(`Part of this page couldn’t be drawn (${m.id}: ${k}). The rest works as usual.`, 'warn', 8000);
        }
        return null;
      }
    };
  }
  return out;
}

// nodes turns what a hook gave into a list of nodes to append.
export function nodes(v) {
  if (v == null || v === false) return [];
  return Array.isArray(v) ? v.flat(Infinity).filter((x) => x != null && x !== false) : [v];
}
