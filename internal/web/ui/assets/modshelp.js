// The pop-up that tells players which mod a world needs and where to
// download it. Valheim can't send mods to players, so each player installs
// them once on their own PC with a mod manager.

import { h, icon, openModal, copyText } from './dom.js';

const MANAGERS = [
  ['r2modman', 'https://thunderstore.io/c/valheim/p/ebkr/r2modman/'],
  ['Gale', 'https://thunderstore.io/c/valheim/p/Kesomannen/GaleModManager/'],
];

// address shows a web address people can read off the screen (through an
// IP KVM a link opens on the server, not on their own PC), with a copy
// button.
function address(url, label) {
  const shown = url.replace(/^https?:\/\//, '').replace(/\/$/, '');
  return h('div', { class: 'address' },
    label ? h('span', { class: 'muted' }, label) : null,
    h('a', { class: 'mono', href: url, target: '_blank', rel: 'noopener' }, shown),
    h('button', { class: 'btn btn-ghost btn-icon btn-sm', type: 'button', 'aria-label': `Copy ${shown}`, onclick: () => copyText(url, 'Address copied') }, icon('copy')));
}

// openModsHelp explains the mods in list (as the status and the worlds list
// give them) to players.
export function openModsHelp(list) {
  if (!list || !list.length) return;
  const many = list.length > 1;
  const it = many ? 'them' : 'it';
  openModal({
    title: many ? 'The mods you need to play' : 'The mod you need to play',
    wide: true,
    body: [
      h('p', null, `This world uses ${many ? 'mods' : 'a mod'}, so everyone who plays needs ${it} in their own Valheim too. Install ${it} once on your PC. Xbox, PlayStation, Switch and Game Pass players can’t use mods.`),
      list.map((m) => h('div', { class: 'mod-box' },
        h('div', { class: 'row', style: { gap: '8px' } },
          h('b', { class: 'mod-name' }, m.name), h('span', { class: 'muted' }, `by ${m.author}`),
          h('span', { class: 'spacer' }),
          m.version ? h('span', { class: 'badge' }, `The server has version ${m.version}`) : null),
        h('p', null, m.summary),
        address(m.url, 'Download page:'))),
      h('h3', { class: 'mod-howto' }, `How to install ${it}`),
      h('ol', { class: 'steps' },
        h('li', null, 'Get a free mod manager, ', h('b', null, 'r2modman'), ' or ', h('b', null, 'Gale'), ':',
          h('div', { class: 'managers' }, MANAGERS.map(([name, url]) => address(url, name)))),
        h('li', null, 'In the mod manager, choose Valheim, search for ', h('b', null, many ? 'each mod' : `“${list[0].name}”`),
          ' and install it (', h('b', null, 'Download'), ' in r2modman). Anything else it needs, such as BepInEx, comes with it.'),
        h('li', null, 'Start Valheim from the mod manager (', h('b', null, 'Start modded'), ' in r2modman) and join as usual.')),
      h('p', { class: 'small muted' }, 'If joining fails because the versions don’t match, update the mod in the mod manager.'),
    ],
    actions: [{ label: 'Close', kind: 'primary' }],
  });
}
