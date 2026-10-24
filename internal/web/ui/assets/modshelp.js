// The pop-up that tells players which mods a world needs and where to
// download them. Valheim can't send mods to players, so each player installs
// them once on their own PC with a mod manager.

import { h, icon, openModal, copyText } from './dom.js';

const GALE = 'https://thunderstore.io/c/valheim/p/Kesomannen/GaleModManager/';
const R2MODMAN = 'https://thunderstore.io/c/valheim/p/ebkr/r2modman/';

const NEED = {
  required: ['warn', 'Needed to join'],
  recommended: ['', 'Recommended'],
};

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

const names = (list) => list.length > 1 ? list.slice(0, -1).map((m) => m.name).join(', ') + ' and ' + list[list.length - 1].name : list[0].name;

// openModsHelp explains the mods in list (as the status and the worlds list
// give them) to players.
export function openModsHelp(list) {
  if (!list || !list.length) return;
  const many = list.length > 1;
  const it = many ? 'them' : 'it';
  const newWorlds = list.filter((m) => m.newWorlds);
  const optional = list.filter((m) => m.players !== 'required');
  openModal({
    title: many ? 'The mods you need to play' : 'The mod you need to play',
    wide: true,
    body: [
      h('p', null, `This world uses ${many ? `${list.length} mods` : 'a mod'}, so everyone who plays installs ${it} once in their own Valheim. `,
        optional.length && optional.length < list.length ? 'You can’t join without the ones marked ' : '',
        optional.length && optional.length < list.length ? h('b', null, 'Needed to join') : '',
        optional.length && optional.length < list.length ? '. ' : '',
        'Xbox, PlayStation, Switch and Game Pass players can’t use mods.'),
      list.map((m) => {
        const [tone, need] = NEED[m.players] || NEED.required;
        return h('div', { class: 'mod-box' },
          h('div', { class: 'row', style: { gap: '8px' } },
            h('b', { class: 'mod-name' }, m.name), h('span', { class: 'muted' }, `by ${m.author}`),
            h('span', { class: 'spacer' }),
            h('span', { class: `badge ${tone}` }, need),
            m.version ? h('span', { class: 'badge' }, `The server has version ${m.version}`) : null),
          h('p', null, m.summary),
          address(m.url, 'Download page:'));
      }),
      h('h3', { class: 'mod-howto' }, `How to install ${it}`),
      h('ol', { class: 'steps' },
        h('li', null, 'Get a free mod manager, ', h('b', null, 'Gale'), ' or ', h('b', null, 'r2modman'), ':',
          h('div', { class: 'managers' }, address(GALE, 'Gale'), address(R2MODMAN, 'r2modman'))),
        h('li', null, 'Choose Valheim and make a new profile for this server. Search for ', h('b', null, many ? 'each mod' : `“${list[0].name}”`),
          ' and install it. Anything else it needs, such as BepInEx, comes with it.'),
        h('li', null, 'Start Valheim from the mod manager (', h('b', null, 'Launch game'), ' in Gale, ', h('b', null, 'Start modded'), ' in r2modman) and join as usual.')),
      newWorlds.length ? h('p', { class: 'small text-2' }, `Play other worlds with a different profile: with ${names(newWorlds)} on, worlds made without it won’t load.`) : null,
      h('p', { class: 'small muted' }, 'If joining fails because the versions don’t match, update the mods in the mod manager.'),
    ],
    actions: [{ label: 'Close', kind: 'primary' }],
  });
}
