// Tutorial: a short guide to starting and stopping the server, opened with
// the Tutorial button on the Home page. Its pictures are made of the panel's
// own buttons and status labels, so they look like the real ones, but they're
// only pictures: nothing in them can be clicked.

import { h, icon, openModal } from './dom.js';
import { restWord } from './app.js';

let isOpen = false;

const b = (text) => h('b', null, text);
const fakeButton = (cls, ic, label) => h('span', { class: `btn ${cls}` }, icon(ic), label);
const pill = (dot, label) => h('span', { class: 'status-pill' }, h('span', { class: `dot ${dot}` }), label);
// press marks the control to click with a mouse pointer.
const press = (el) => h('span', { class: 'tut-press' }, el, icon('pointer', 'tut-pointer'));

// flow shows stages from left to right with arrows between them (top to
// bottom where there's no room). Each stage is [picture, caption].
function flow(...stages) {
  const col = (el, c) => { el.style.setProperty('--c', c); return el; };
  return h('div', { class: 'tut-flow' }, stages.map(([pic, caption], i) => [
    i ? col(h('span', { class: 'tut-arrow' }, icon('arrowRight')), 2 * i) : null,
    col(h('div', { class: 'tut-pic' }, pic), 2 * i + 1),
    col(h('span', { class: 'tut-caption' }, caption), 2 * i + 1),
  ]));
}

function joinPicture(crossplay) {
  return h('div', { class: 'tut-join' },
    crossplay
      ? [h('span', { class: 'muted small' }, 'Join code'), h('span', { class: 'join-code' }, '123 456')]
      : [h('span', { class: 'muted small' }, 'Address'), h('span', { class: 'mono tut-addr' }, '203.0.113.25:2456')],
    h('span', { class: 'small' }, h('span', { class: 'muted' }, 'Password '), h('span', { class: 'pw' }, '••••••••')));
}

const worldCard = (name, playing, extra) =>
  h('div', { class: `tut-world${playing ? ' playing' : ''}` }, h('span', { class: 'world-name' }, name), extra);

// The world card's ⋯ menu, and the Difficulty window it opens.
const worldMenu = () => worldCard('Midgard', false, h('div', { class: 'tut-menu' },
  h('span', null, icon('archive'), 'Backups'),
  press(h('span', null, icon('gauge'), 'Difficulty')),
  h('span', null, icon('trash'), 'Delete…')));

const check = (on, label) => h('span', { class: `tut-check${on ? ' on' : ''}` }, h('span', { class: 'box' }, on ? icon('check') : null), label);
const difficultyWindow = () => h('div', { class: 'tut-dialog' },
  h('span', { class: 'muted small' }, 'Difficulty'),
  h('span', { class: 'tut-select' }, 'Hard', icon('chevronDown')),
  check(false, 'No map'),
  check(true, 'Fire hazard'));

function steps(st) {
  const rest = restWord(st);
  const sleep = rest === 'Sleep';
  const crossplay = !st || st.join.crossplay;
  const mods = (st && st.mods) || [];
  return [
    {
      icon: 'gauge',
      title: 'Set the world’s difficulty',
      picture: flow(
        [worldMenu(), 'Worlds, then ⋯ on the world'],
        [difficultyWindow(), 'Pick a preset and switches']),
      text: [
        ['Best done before a world is first played, but you can change it any time. Open ', b('Worlds'), ', click ', b('⋯'), ' on the world’s card and choose ', b('Difficulty'), '. A new world gets the same choices under ', b('New world'), '.'],
        ['Pick a preset such as Casual or Hard. Under ', b('More options'), ' you can change combat, resources, raids and portals, and tick switches like ', b('No map'), ' or ', b('Fire hazard'), '.'],
        ['Changes apply the next time the server starts the world. Mods, such as Advanced Portals, are in ', b('Settings'), '.'],
      ],
    },
    {
      icon: 'play',
      title: 'Start the world',
      picture: flow(
        [press(fakeButton('btn-primary btn-lg', 'play', 'Start server')), 'Click it'],
        [pill('warn', 'Starting…'), 'The world loads'],
        [pill('good', 'Online'), 'Ready to play']),
      text: [
        ['On the Home page, click ', b('Start server'), '. The server loads the world shown next to ', b('World'), '.'],
        ['This takes a minute or two, longer after a Valheim update. When the light turns green and it says ', b('Online'), ', the world is ready.'],
      ],
    },
    {
      icon: 'users',
      title: 'Join from Valheim',
      picture: flow(
        [h('div', { class: 'tut-game' }, h('span', { class: 'muted small' }, 'Valheim'), press(h('span', { class: 'tut-game-button' }, 'Join Game'))), 'In the game'],
        [joinPicture(crossplay), 'From the Home page']),
      text: [
        ...(crossplay
          ? [
            ['In Valheim, pick your character and go to ', b('Join Game'), '. Choose to join by code and type the ', b('join code'), ' from ', b('How friends join'), ' on the Home page, then the password.'],
            ['The join code changes every time the server starts, so check it again after a restart.'],
          ]
          : [
            ['In Valheim, pick your character and go to ', b('Join Game'), '. Add the server by the address from ', b('How friends join'), ' on the Home page, then enter the password.'],
          ]),
        ...(mods.length
          ? [[`This server uses the ${mods.join(', ')} mod${mods.length > 1 ? 's' : ''}, so install the same in your game first, with a mod manager such as r2modman or Gale.`]]
          : []),
      ],
    },
    {
      icon: 'stop',
      title: 'Stop the world',
      picture: flow(
        [press(fakeButton('btn-lg', 'stop', 'Stop server')), 'Click it'],
        [pill('warn', 'Stopping…'), 'The world is saved'],
        [pill('', 'Offline'), 'Safe to turn off']),
      text: [
        ['Click ', b('Stop server'), '. The world is saved first, then the server shuts down. If anyone is still playing, the panel asks first, because they’ll be disconnected.'],
        ['Wait until it says ', b('Offline'), ' before you turn off or restart the computer.'],
        ['Closing this window doesn’t stop the server. It keeps running, and the shield icon next to the clock opens the panel again.'],
      ],
    },
    {
      icon: 'globe',
      title: 'Play a different world',
      picture: flow(
        [worldCard('Midgard', true, h('span', { class: 'badge good' }, 'Running now')), 'The world it plays now'],
        [worldCard('Ashlands Run', false, press(fakeButton('btn-primary btn-sm', 'play', 'Switch to this world'))), 'Click to play this one']),
      text: [
        ['The server plays one world at a time. To change it, open ', b('Worlds'), ' and click ', b('Play this world'), ' on the one you want.'],
        ['While the server is running, that button says ', b('Switch to this world'), '. The server saves the current world and restarts with the new one, which takes about a minute.'],
      ],
    },
    {
      icon: 'moon',
      title: 'Done for the day',
      picture: flow(
        [press(fakeButton('', 'moon', rest)), 'Click it'],
        [h('span', { class: 'status-pill' }, icon('moon'), sleep ? 'Asleep' : 'Hibernating'), sleep ? 'Uses very little power' : 'Uses no power'],
        [pill('good', 'Online'), 'Back after waking up']),
      text: [
        ['Click ', b(rest), ' at the top of any page. It saves the world, stops the server and ', sleep ? 'puts the computer to sleep.' : 'hibernates the computer.'],
        ['To play again, wake the computer with Wake-on-LAN from the IP KVM, or press its power button. If the server was running, it starts again by itself.'],
        ['You can open this tutorial again any time with the ', b('Tutorial'), ' button on the Home page.'],
      ],
    },
  ];
}

export function openTutorial(app) {
  if (isOpen) return;
  isOpen = true;
  const list = steps(app.status);
  const last = list.length - 1;
  let at = 0;

  // Every page is drawn in the same place and only the current one shows, so
  // the window keeps its size and the buttons stay put from step to step.
  const pages = list.map((s, i) => h('section', { class: 'tut-page' },
    h('div', { class: 'tut-figure', inert: true, 'aria-hidden': 'true' }, s.picture),
    h('div', { class: 'tut-head' },
      h('span', { class: 'tut-icon' }, icon(s.icon)),
      h('div', null, h('div', { class: 'tut-step' }, `Step ${i + 1} of ${list.length}`), h('h3', null, s.title))),
    h('div', { class: 'tut-text' }, s.text.map((p) => h('p', null, p)))));
  const live = h('p', { class: 'sr-only', 'aria-live': 'polite' });
  const dots = h('div', { class: 'tut-dots' }, list.map((s, i) =>
    h('button', { type: 'button', 'aria-label': `Step ${i + 1}: ${s.title}`, onclick: () => show(i) })));

  const onKey = (e) => {
    if (e.key === 'ArrowRight') show(at + 1);
    else if (e.key === 'ArrowLeft') show(at - 1);
    else return;
    e.preventDefault();
  };
  const modal = openModal({
    title: 'Tutorial',
    wide: true,
    body: [h('div', { class: 'tut-pages' }, pages), live],
    actions: [{ label: 'Back' }, { label: 'Next', kind: 'primary' }],
    onClose: () => {
      isOpen = false;
      document.removeEventListener('keydown', onKey);
    },
  });
  // Plain handlers: the dialog's own ones disable a button while it works,
  // which would take the keyboard focus off Next at every step.
  const [back, next] = modal.foot.children;
  back.onclick = () => show(at - 1);
  next.onclick = () => (at === last ? modal.close() : show(at + 1));
  modal.foot.prepend(dots);
  modal.el.classList.add('tutorial');
  const scroller = modal.el.querySelector('.modal-body');
  document.addEventListener('keydown', onKey);

  function show(i) {
    at = Math.max(0, Math.min(last, i));
    pages.forEach((p, n) => {
      p.classList.toggle('on', n === at);
      if (n === at) p.removeAttribute('aria-hidden');
      else p.setAttribute('aria-hidden', 'true');
    });
    [...dots.children].forEach((d, n) => {
      d.className = n === at ? 'on' : n < at ? 'seen' : '';
      d.setAttribute('aria-current', n === at ? 'step' : 'false');
    });
    back.hidden = at === 0;
    back.replaceChildren(icon('arrowLeft'), 'Back');
    next.replaceChildren(...(at === last ? [icon('check'), 'Done'] : ['Next', icon('arrowRight')]));
    live.textContent = `Step ${at + 1} of ${list.length}: ${list[at].title}`;
    scroller.scrollTop = 0;
    if (document.activeElement === back && back.hidden) next.focus();
  }
  show(0);
}
