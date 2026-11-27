// A game's page (spec 6.1): its tabs Home, Worlds, Players, Stats, Mods
// (only for a game that has mods), Settings and Log. Each tab is a module
// with render(app, game) and, if it needs them, mount, unmount and
// afterRender; a game's own module may replace any tab (games/README.md).
// The game is always that instance's entry in the latest status, so a tab
// that is reused for another game never shows the first one's data: every
// tab keeps its state by instance.

import { h, icon } from './dom.js';
import { moduleFor, isLoaded } from './games.js';
import { gameByID, tile, enc } from './state.js';
import home from './tab-home.js';
import worlds from './tab-worlds.js';
import players from './tab-players.js';
import stats from './tab-stats.js';
import settings from './tab-settings.js';
import logTab from './tab-log.js';

export const TABS = ['home', 'worlds', 'players', 'stats', 'mods', 'settings', 'log'];
const LABELS = { home: 'Home', worlds: 'Worlds', players: 'Players', stats: 'Stats', mods: 'Mods', settings: 'Settings', log: 'Log' };
const ICONS = { home: 'home', worlds: 'globe', players: 'users', stats: 'chart', mods: 'wrench', settings: 'sliders', log: 'terminal' };
const SHELL = { home, worlds, players, stats, settings, log: logTab };

// noMods is the Mods tab of a game without one (an old link, say).
const noMods = {
  render(app, g) {
    return h('div', { class: 'card empty' }, icon('wrench'), `${g.short} has no mods to set up here.`,
      h('div', { style: { marginTop: '12px' } }, h('a', { class: 'btn btn-sm', href: `#/game/${enc(g.id)}/home` }, 'Go to its Home')));
  },
};
const waiting = { render: () => h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…')) };

// hasMods reports whether a game shows a Mods tab: its module draws one
// (the status may also say so, once it carries Info.Mods).
export function hasMods(g) {
  return !!(g.mods || moduleFor(g.game).tabs.mods);
}

function tabFor(g, tab) {
  const mod = moduleFor(g.game);
  const own = mod.tabs[tab];
  if (own && typeof own.render === 'function') return own;
  if (tab === 'mods') return isLoaded(g.game) ? noMods : waiting;
  return SHELL[tab];
}

function tabsBar(g, tab) {
  const shown = TABS.filter((t) => t !== 'mods' || hasMods(g) || tab === 'mods');
  const mod = moduleFor(g.game);
  return h('nav', { class: 'tabs', 'aria-label': `${g.short} pages` },
    shown.map((t) => h('a', { href: `#/game/${enc(g.id)}/${t}`, class: t === tab ? 'active' : null, 'aria-current': t === tab ? 'page' : null, 'data-key': 'tab:' + t },
      icon(ICONS[t]), (mod.tabs[t] && mod.tabs[t].title) || LABELS[t])));
}

// gamePage is the page for one instance and tab.
export function gamePage(id, tab) {
  let mounted = null; // the tab module that is mounted
  let last = null;    // the game as it was when last drawn
  const unmount = (app) => {
    if (mounted && mounted.unmount) {
      try { mounted.unmount(app, last); } catch (e) { console.error(e); }
    }
    mounted = null;
  };
  return {
    heading(app) {
      const g = gameByID(app.status, id);
      return g ? [tile(g, 'lg'), h('span', null, g.short)] : 'Game';
    },
    render(app) {
      const g = gameByID(app.status, id);
      if (!g) {
        return h('section', { class: 'card empty-state' },
          h('h2', null, 'This game isn’t on this computer'),
          h('p', { class: 'text-2' }, 'It may have been removed. Add it again to bring back its worlds.'),
          h('div', { class: 'row' }, h('a', { class: 'btn btn-primary', href: '#/add' }, icon('plus'), 'Add a game'), h('a', { class: 'btn', href: '#/overview' }, 'Overview')));
      }
      last = g;
      const t = tabFor(g, tab);
      if (t !== mounted) {
        // A game's module can arrive after the page opened, with its own
        // version of this tab: the shell's is then swapped for it.
        unmount(app);
        mounted = t;
        if (t.mount) queueMicrotask(() => { if (mounted === t) t.mount(app, gameByID(app.status, id) || g); });
      }
      let body;
      try {
        body = t.render(app, g);
      } catch (e) {
        console.error(e);
        body = h('div', { class: 'callout bad' }, icon('alert'), h('div', { class: 'body' }, h('p', null, 'This tab couldn’t be drawn: ' + (e && e.message))));
      }
      return h('div', { class: 'gamepage', style: { '--g': g.color } }, tabsBar(g, tab), h('div', { class: 'tab-body', 'data-key': 'tab-body:' + tab }, body));
    },
    unmount,
    afterRender(app) {
      const g = gameByID(app.status, id);
      if (g && mounted && mounted.afterRender) mounted.afterRender(app, g);
    },
    onJobDone(app, job) {
      const g = gameByID(app.status, id);
      if (g && mounted && mounted.onJobDone && (!job.game || job.game === id)) mounted.onJobDone(app, g, job);
    },
    ownsJob(job) {
      return !!(mounted && mounted.ownsJob && mounted.ownsJob(job));
    },
  };
}
