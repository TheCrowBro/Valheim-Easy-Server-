// Valheim's Mods tab (spec 6.1: "Valheim's mod checklist per world and the
// mods help"). On the old panel these sat on the Worlds page and in the
// How friends join card: each world's mods line with Add mods / Change
// and Player instructions, and the pop-up that tells players what to
// install, with the mods code that imports them all at once. Here they
// are together, with the same dialogs (from worlds.js and modshelp.js) and
// the same words.

import { h, icon } from '../../dom.js';
import { modsOf, busy } from './common.js';
import { load, worldsOf, modsLine, worldsView } from './worlds.js';
import { modsNotice } from './join.js';

function worldRow(app, game, name, data) {
  const running = data.running === name;
  const selected = data.selected === name;
  const exists = data.worlds.some((w) => w.name === name);
  return h('article', { class: `card world mods-world${selected ? ' selected' : ''}`, 'data-key': 'mods:' + name },
    h('div', { class: 'row' },
      h('div', { class: 'world-name' }, name),
      h('span', { class: 'spacer' }),
      running ? h('span', { class: 'badge good' }, 'Running now') : selected ? h('span', { class: 'badge accent' }, 'Selected') : null,
      exists ? null : h('span', { class: 'badge warn' }, 'Not generated yet')),
    modsLine(app, game, name, busy(game)));
}

export const modsTab = {
  title: 'Mods',
  render(app, game) {
    worldsView.set(app);
    const data = worldsOf(game);
    if (!data) return h('div', { class: 'vh' }, h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading mods…')));
    const { mods, code } = modsOf(game, app);
    const names = data.worlds.map((w) => w.name);
    // The selected world comes first, even before the server has made it.
    if (data.selected && !names.includes(data.selected)) names.unshift(data.selected);
    else if (data.selected) names.sort((a, b) => (a === data.selected ? -1 : b === data.selected ? 1 : 0));
    return h('div', { class: 'vh stack' },
      h('section', { class: 'card' },
        h('div', { class: 'card-head' }, icon('wrench'), h('h2', null, 'Mods')),
        h('p', { class: 'text-2' }, 'Each world is played with its own mods. Tick the mods to play a world with: the server downloads them when it starts that world, and only uses them for that world.'),
        h('p', { class: 'text-2', style: { marginTop: '8px' } }, 'Valheim can’t send mods to players, so each player installs them once on their own PC with a mod manager. ',
          h('b', null, 'Player instructions'), ' says which ones and where to get them.'),
        game.runningWorld && mods.length ? h('div', { style: { marginTop: '14px' } }, modsNotice(mods, code))
          : game.runningWorld ? h('p', { class: 'small muted', style: { marginTop: '12px' } }, `“${game.runningWorld}” is running without mods: plain Valheim.`) : null),
      h('div', { class: 'mods-worlds' }, names.map((name) => worldRow(app, game, name, data))));
  },
  mount(app, game) {
    worldsView.set(app);
    load(game, true);
  },
};
