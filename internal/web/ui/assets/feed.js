// The activity feed: what happened, newest first, for every game (the
// Overview) or one game (its Home). Each entry has a kind; the shell styles
// its own kinds and the shared game kinds, and a game's module gives its
// own kinds an icon (feedIcons). A kind nobody knows shows as a warning,
// so nothing that happened is hidden.

import { h, icon, ago } from './dom.js';
import { moduleFor } from './games.js';
import { tile, gameByID, legacyID } from './state.js';

export const FEED_STYLE = {
  join: ['login', 'good'], leave: ['logout', ''], death: ['skull', 'bad'], online: ['power', 'good'],
  stopped: ['stop', ''], stopping: ['stop', ''], crashed: ['alert', 'bad'], saved: ['save', ''],
  count: ['users', ''], code: ['key', 'accent'], turned_away: ['userX', 'warn'],
  hibernate: ['moon', 'accent'], wake: ['sun', 'accent'], backup: ['archive', 'accent'], restore: ['archive', 'accent'],
  import: ['upload', 'accent'], delete: ['trash', ''], world: ['globe', 'accent'], update: ['refresh', 'accent'],
  warning: ['alert', 'warn'], starting: ['play', ''], info: ['info', ''], discord: ['message', 'accent'],
};

// The app's own entries (the computer resting and waking) are about no
// game. The legacy game's entries don't name it either (the activity feed
// is one of the old files, 1.3), so any other entry without a game is its.
const APP_KINDS = new Set(['hibernate', 'wake']);
function gameOf(st, a) {
  if (a.game) return gameByID(st, a.game);
  return APP_KINDS.has(a.kind) ? null : gameByID(st, legacyID(st));
}

// styleFor is an entry's [icon, tone].
function styleFor(st, a) {
  const g = gameOf(st, a);
  const own = g ? moduleFor(g.game).feedIcons[a.kind] : null;
  if (own) return Array.isArray(own) ? [own[0], own[1] || ''] : [own, ''];
  return FEED_STYLE[a.kind] || ['alert', 'warn'];
}

// feedCard is the Activity card. gameID limits it to one game; without it
// each entry shows which game it was about.
export function feedCard(st, gameID) {
  const items = (st.activity || []).filter((a) => {
    if (!gameID) return true;
    const g = gameOf(st, a);
    return g ? g.id === gameID : gameID === legacyID(st);
  });
  return h('section', { class: 'card', 'aria-label': 'Activity' },
    h('div', { class: 'card-head' }, h('h2', null, 'Activity')),
    items.length
      ? h('div', { class: 'feed' }, items.map((a) => {
        const [ic, tone] = styleFor(st, a);
        const g = gameOf(st, a);
        const allow = a.kind === 'turned_away' && a.name && g
          ? h('button', { class: 'btn btn-sm', 'data-action': 'allow', 'data-id': g.id, 'data-name': a.name, title: `Let ${a.name} join from now on` }, icon('check'), 'Allow')
          : null;
        return h('div', { class: 'feed-item', 'data-key': String(a.seq) },
          h('div', { class: `feed-icon ${tone}` }, icon(ic)),
          h('div', { class: 'feed-text' }, !gameID && g ? h('span', { class: 'feed-game' }, tile(g, 'xs'), g.short) : null, a.text,
            allow ? h('div', { style: { marginTop: '6px' } }, allow) : null),
          h('div', { class: 'feed-time', title: new Date(a.time).toLocaleString() }, ago(a.time)));
      }))
      : h('div', { class: 'empty' }, gameID ? 'Nothing has happened in this game yet.' : 'Nothing has happened yet.'));
}
