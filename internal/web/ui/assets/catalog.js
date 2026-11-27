// The games this version of the app knows (GET /api/catalog): what Add a
// game lists, and the names of each game's ports. Loaded when a page needs
// it, and again after a game is added or removed.

import { api } from './dom.js';
import { load as loadModules } from './games.js';

let list = null;
let at = 0;
let pending = null;

// loadCatalog resolves to the list; force asks the app again.
export function loadCatalog(force) {
  if (pending) return pending;
  if (list && !force && Date.now() - at < 60000) return Promise.resolve(list);
  pending = api('/api/catalog')
    .then((l) => {
      list = Array.isArray(l) ? l : [];
      at = Date.now();
      loadModules(list.map((c) => c.id));
      return list;
    })
    .finally(() => { pending = null; });
  return pending;
}

// catalog is the list as last loaded, or null.
export const catalog = () => list;

export const catalogGame = (id) => (list || []).find((c) => c.id === id) || null;

// portLabel is what a game calls one of its ports ("Game port").
export function portLabel(gameID, role) {
  const c = catalogGame(gameID);
  const p = c && (c.ports || []).find((x) => x.role === role);
  if (p && p.label) return p.label;
  return role ? role[0].toUpperCase() + role.slice(1) + ' port' : 'Port';
}
