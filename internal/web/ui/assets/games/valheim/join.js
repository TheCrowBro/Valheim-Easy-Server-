// Valheim's parts of "How friends join" on its Home tab, moved from the old
// panel's Home page (home.js joinCard and the notices beside it).
//
// joinExtras is the contract's hook: the shell draws the card from the
// game's join (code, address, password, Copy, steps) and puts these at
// its end. They are what only Valheim can say: why there is no join code
// (crossplay that can't start, a code that doesn't come), the mods
// players need with the mods code, and the Tutorial button, which sat
// next to Start on the old Home.
//
// joinCard is the whole card as the old Home drew it (the join code in
// two groups of three, "Or address", the password with show and copy,
// Valheim's own steps, and the notices). The contract doesn't have it:
// it is here so the shell can draw Valheim's card exactly as today if the
// lead adds the hook (reported); until then nothing calls it.
//
// Both are drawn on every refresh, only from the game's entry (and the
// legacy status the module last saw), and never call the app; a button
// does its work when it is clicked.

import { h, icon, copyText, clock } from '../../dom.js';
import { modsOf, freshStatus, legacyStatus } from './common.js';
import { openModsHelp } from './modshelp.js';
import { openTutorial } from './tutorial.js';

const shownPw = new Map(); // instance -> the password is shown

const settingsHash = (game) => `#/game/${encodeURIComponent(game.id)}/settings`;

export function joinExtras(game) {
  if (!game || !game.join) return null;
  const j = game.join;
  const out = [];
  if (j.crossplay && !j.code && j.problem) out.push(codeProblem(game, j.problem, j.port));
  const { mods, code } = modsOf(game);
  if (mods.length) out.push(modsNotice(mods, code));
  out.push(guide(game));
  return h('div', { class: 'vh join-extras' }, out);
}

// guide is the Tutorial button, with what it is for.
function guide(game) {
  return h('div', { class: 'vh-guide' },
    h('button', {
      class: 'btn btn-sm btn-guide', type: 'button', title: 'A short guide to starting and stopping the server',
      onclick: async () => openTutorial(await freshStatus(), game),
    }, icon('help'), 'Tutorial'),
    h('span', { class: 'small muted' }, 'A short guide to starting and stopping the server.'));
}

export function joinCard(game) {
  const j = game.join;
  const online = game.state === 'online';
  const shown = !!shownPw.get(game.id);
  const pw = j.password || '';
  const pwText = h('span', { class: 'pw' }, shown ? pw : '•'.repeat(Math.min(12, pw.length)));
  const eye = h('button', {
    class: 'btn btn-ghost btn-icon btn-sm', type: 'button', 'aria-label': shown ? 'Hide password' : 'Show password',
    // Shown or hidden here, in place, and remembered for this game, so
    // the regular refresh doesn't hide it again.
    onclick: () => {
      const now = !shownPw.get(game.id);
      shownPw.set(game.id, now);
      pwText.textContent = now ? pw : '•'.repeat(Math.min(12, pw.length));
      eye.replaceChildren(icon(now ? 'eyeOff' : 'eye'));
      eye.setAttribute('aria-label', now ? 'Hide password' : 'Show password');
    },
  }, icon(shown ? 'eyeOff' : 'eye'));
  const copyBtn = (text, label, aria) => h('button', {
    class: 'btn btn-ghost btn-icon btn-sm', type: 'button', 'aria-label': aria, onclick: () => copyText(text, label),
  }, icon('copy'));
  const pwRow = [
    h('dt', null, 'Password'),
    h('dd', null, pwText),
    h('dd', { class: 'row', style: { gap: '4px' } }, eye, copyBtn(pw, 'Password copied', 'Copy password')),
  ];
  const addrRow = (label, addr) => [
    h('dt', null, label), h('dd', { class: 'mono' }, addr),
    h('dd', null, copyBtn(addr, 'Address copied', 'Copy address')),
  ];
  const lan = (j.lan || []).filter((a) => !a.startsWith('100.'));
  const body = [];
  if (j.crossplay && !j.code && j.problem === 'crossplay') {
    body.push(codeProblem(game, j.problem, j.port)); // nobody can join, by code or address
  } else if (j.crossplay) {
    const code = j.code ? j.code.replace(/(\d{3})(\d{3})/, '$1 $2') : '';
    if (!code && j.problem) body.push(codeProblem(game, j.problem, j.port));
    body.push(h('div', { class: 'row', style: { justifyContent: 'space-between', marginBottom: '14px' } },
      h('div', null, h('div', { class: 'muted small' }, 'Join code'),
        code ? h('div', { class: 'join-code' }, code)
          : h('div', { class: 'text-2', style: { padding: '8px 0' } }, j.problem ? 'No join code' : online || game.state === 'starting' ? 'Waiting for the code…' : 'Shown here when the server is online.')),
      code ? h('button', { class: 'btn', type: 'button', onclick: () => copyText(j.code, 'Join code copied') }, icon('copy'), 'Copy code') : null));
    body.push(h('dl', { class: 'kv' }, j.address ? addrRow('Or address', j.address) : null, pwRow));
    body.push(h('ol', { class: 'steps' },
      h('li', null, 'Open Valheim, pick your character and go to ', h('b', null, 'Join Game'), '.'),
      h('li', null, 'Choose to join by code and enter the code above (or add the server by its address).'),
      h('li', null, 'Enter the password. The code changes each time the server restarts.')));
  } else {
    body.push(h('dl', { class: 'kv' },
      j.address ? addrRow('Address', j.address) : null,
      lan.length ? addrRow('Same network', lan[0]) : null,
      pwRow));
    body.push(h('ol', { class: 'steps' },
      h('li', null, 'In Valheim go to ', h('b', null, 'Join Game'), ', add the server by its address and enter the password.'),
      h('li', null, 'Friends outside your home need UDP ports ', h('b', null, `${j.port}–${j.port + 1}`), ' forwarded on your router to this computer. Or turn on Crossplay in Settings to get a join code that works without port forwarding.')));
  }
  body.push(discordLine(game));
  const { mods, code } = modsOf(game);
  if (mods.length) body.unshift(modsNotice(mods, code));
  if (online && (game.reminder || []).length) body.unshift(reminderNotice(game));
  body.push(guide(game));
  return h('div', { class: 'vh join-card' },
    h('div', { class: 'card-head' }, h('h2', null, 'How friends join'),
      h('span', { class: 'badge' }, j.crossplay ? 'Crossplay on' : 'Steam only')),
    body);
}

// reminderNotice says what friends need to do before joining, because
// something changed since they last played.
function reminderNotice(game) {
  const text = game.reminder.join(', ');
  const st = legacyStatus(game);
  const discord = game.discord || (st && st.discord);
  return h('div', { class: 'callout warn reminder' }, icon('alert'), h('div', { class: 'body small' },
    h('p', null, h('strong', null, 'Before your friends join: '), text[0].toUpperCase() + text.slice(1) + '.'),
    h('p', { class: 'text-2' }, 'Things changed since they last played. ', discord ? 'The Discord post says this too.' : 'Let them know.')));
}

// codeProblem says why there's no join code and what to do instead. port is
// the game port, which can be changed in Settings.
function codeProblem(game, problem, port) {
  const off = ['Turn ', h('b', null, 'Crossplay'), ' off in Settings and restart the server: friends then join by address, with UDP ports ', `${port}–${port + 1}`, ' forwarded on your router to this computer.'];
  const text = problem === 'crossplay'
    ? [h('p', null, h('strong', null, 'Crossplay can’t start on this computer, so there’s no join code and nobody can join. '),
      'Windows couldn’t load Valheim’s crossplay part (Party.dll).'),
    h('p', null, ...off, ' Installing Microsoft’s Visual C++ Redistributable (x64) fixes this on some computers.')]
    : [h('p', null, h('strong', null, 'No join code yet. '), 'Valheim usually gets one within a minute of starting. The Log page says why if you type “playfab” in its filter.'),
      h('p', null, 'If it doesn’t come: ', ...off)];
  return h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body small' }, text,
    h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: settingsHash(game) }, 'Open settings'))));
}

// discordLine says whether the join code made it to the Discord channel.
function discordLine(game) {
  const st = legacyStatus(game);
  const d = game.discord || (st && st.discord);
  if (!d || game.state !== 'online' || game.join.problem === 'crossplay') return null;
  if (d.error) {
    return h('div', { class: 'callout warn discord-line' }, icon('alert'), h('div', { class: 'body small' },
      h('p', null, h('strong', null, 'Couldn’t post in Discord. '), d.error),
      h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: settingsHash(game) }, 'Open settings'))));
  }
  const text = d.posted ? `Posted in Discord at ${clock(d.posted)}.`
    : game.join.crossplay && !game.join.code ? 'The join code is posted in Discord when it’s ready.' : 'Posting in Discord…';
  return h('p', { class: 'small muted discord-line' }, icon(d.posted ? 'check' : 'message'), text);
}

// modsNotice says the world needs mods; its button opens the pop-up with
// which ones and where to download them. code imports them all at once in
// a mod manager.
export function modsNotice(mods, code) {
  const named = (list) => (list.length > 3 ? `${list.length} mods`
    : `the ${list.map((m) => m.name).join(list.length > 2 ? ', ' : ' and ').replace(/, ([^,]*)$/, ' and $1')} mod${list.length > 1 ? 's' : ''}`);
  const server = mods.filter((m) => m.players !== 'players');
  const extras = mods.filter((m) => m.players === 'players'); // just for players' own games
  const many = mods.length > 1;
  const optional = extras.length > 1 ? 'They’re optional' : 'It’s optional';
  const text = server.length
    ? [h('strong', null, `This world is played with ${named(server)}. `), `Players install ${server.length > 1 ? 'them' : 'it'} once on their own PC.`,
      extras.length ? ` ${extras.map((m) => m.name).join(' and ')} ${extras.length > 1 ? 'are' : 'is'} optional, just for their own game.` : '']
    : [h('strong', null, `Players can add ${named(extras)} to their own game. `), `${optional}: the server doesn’t need ${many ? 'them' : 'it'}.`];
  return h('div', { class: 'callout info mods-help' }, icon('info'), h('div', { class: 'body small' },
    h('p', null, text),
    code ? h('p', { class: 'mods-code' }, 'Mods code for r2modman or Gale: ', h('span', { class: 'mono' }, code), ' ',
      h('button', { class: 'btn btn-ghost btn-sm', type: 'button', onclick: () => copyText(code, 'Mods code copied') }, icon('copy'), 'Copy')) : null,
    h('div', { class: 'actions' },
      h('button', { class: 'btn btn-sm btn-primary', type: 'button', onclick: () => openModsHelp(mods, code) }, icon('help'),
        !server.length ? (many ? 'About these mods' : 'About this mod') : many ? 'Which mods do I need?' : 'Which mod do I need?'))));
}
