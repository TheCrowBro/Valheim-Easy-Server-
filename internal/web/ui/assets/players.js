// Players: who's online, everyone who has played, and the admin / banned /
// allowed lists.

import { h, icon, api, toast, confirmDialog, dur, ago, initials } from './dom.js';

let known = null;
let lists = null;
let appRef = null;
let timer = null;

const LIST_INFO = {
  admins: ['Admins', 'shield', 'Admins can use server commands in Valheim’s console (press F5 in the game), such as kick, ban and save.'],
  banned: ['Banned', 'ban', 'These players can’t join.'],
  permitted: ['Allowed players only', 'key', 'Leave this empty to let anyone with the password join. If you add anyone here, only these players can join.'],
};

async function load() {
  try {
    const [p, l] = await Promise.all([api('/api/players'), api('/api/lists')]);
    known = p.players;
    lists = l;
  } catch (e) {
    if (!known) toast(e.message, 'bad');
  }
  if (appRef && appRef.page.id === 'players') appRef.render();
}

async function saveList(kind, ids) {
  try {
    await api('/api/lists', { method: 'POST', body: { kind, ids } });
    lists[kind] = ids;
    toast('Saved. Changes apply the next time the server starts.', 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  load();
}

function inList(kind, id) { return !!(lists && id && lists[kind] && lists[kind].includes(id)); }

function onlineCard(app) {
  const st = app.status;
  if (!st) return null;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Playing now'), h('span', { class: 'hint' }, `${st.players.length} of 10`)),
    st.players.length ? h('div', { class: 'list' }, st.players.map((p) => h('div', { class: 'list-row', 'data-key': p.name },
      h('div', { class: 'avatar' }, initials(p.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, p.name),
        h('div', { class: 'sub' }, `Joined ${ago(p.since)} · playing for ${dur((Date.now() - new Date(p.since)) / 1000)}`))))) :
      h('div', { class: 'empty' }, icon('users'), st.state === 'online' ? 'Nobody’s playing right now.' : 'The server is offline.'));
}

function playedSeconds(app, p) {
  const online = app.status && app.status.players.find((x) => x.name === p.name);
  return (p.playSeconds || 0) + (online ? (Date.now() - new Date(online.since)) / 1000 : 0);
}

function knownCard(app) {
  if (!known) return h('section', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…'));
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Everyone who has played'), h('span', { class: 'hint' }, `${known.length}`)),
    known.length ? h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Player'), h('th', null, 'Player ID'), h('th', null, 'Last seen'),
        h('th', { class: 'num' }, 'Time played'), h('th', { class: 'num' }, 'Visits'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
      h('tbody', null, known.map((p) => {
        const admin = inList('admins', p.platformId);
        const banned = inList('banned', p.platformId);
        return h('tr', { 'data-key': p.name },
          h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap' } }, h('div', { class: 'avatar', style: { width: '30px', height: '30px', fontSize: '12px' } }, initials(p.name)),
            h('b', null, p.name), admin ? h('span', { class: 'badge accent' }, 'Admin') : null, banned ? h('span', { class: 'badge bad' }, 'Banned') : null)),
          h('td', null, p.platformId ? h('span', { class: 'copyable' }, h('span', { class: 'mono small' }, p.platformId),
            h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'copy', 'data-text': p.platformId, 'data-label': 'Player ID copied', 'aria-label': 'Copy player ID' }, icon('copy'))) : h('span', { class: 'muted' }, 'unknown')),
          h('td', null, ago(p.lastSeen)),
          h('td', { class: 'num' }, playedSeconds(app, p) >= 60 ? dur(playedSeconds(app, p)) : '–'),
          h('td', { class: 'num' }, p.sessions || 0),
          h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap', justifyContent: 'flex-end' } },
            h('button', { class: 'btn btn-sm', disabled: !p.platformId, 'data-action': 'player-admin', 'data-id': p.platformId || '', 'data-name': p.name },
              icon('shield'), admin ? 'Remove admin' : 'Make admin'),
            h('button', { class: `btn btn-sm${banned ? '' : ' btn-danger'}`, disabled: !p.platformId, 'data-action': 'player-ban', 'data-id': p.platformId || '', 'data-name': p.name },
              icon('ban'), banned ? 'Unban' : 'Ban'))));
      })))) : h('div', { class: 'empty' }, 'Players appear here after they join the server.'));
}

function listCard(kind) {
  const [title, ic, help] = LIST_INFO[kind];
  const ids = (lists && lists[kind]) || [];
  const input = h('input', { class: 'input', placeholder: 'e.g. Steam_76561198012345678', 'data-list-input': kind, 'aria-label': `Add to ${title}` });
  return h('section', { class: 'card', 'data-key': 'list:' + kind },
    h('div', { class: 'card-head' }, icon(ic), h('h2', null, title)),
    h('p', { class: 'small text-2', style: { marginBottom: '12px' } }, help),
    ids.length ? h('div', { class: 'chips', style: { marginBottom: '12px' } }, ids.map((id) => h('span', { class: 'chip', 'data-key': id }, id,
      h('button', { 'data-action': 'list-remove', 'data-kind': kind, 'data-id': id, 'aria-label': `Remove ${id}` }, icon('x'))))) :
      h('p', { class: 'small muted', style: { marginBottom: '12px' } }, 'Nobody yet.'),
    h('div', { class: 'input-group' }, input,
      h('button', { class: 'btn', 'data-action': 'list-add', 'data-kind': kind }, icon('plus'), 'Add')));
}

export default {
  id: 'players',
  title: 'Players',
  icon: 'users',
  render(app) {
    appRef = app;
    return h('div', { class: 'stack' },
      h('div', { class: 'grid grid-2' }, onlineCard(app), h('section', { class: 'card' },
        h('div', { class: 'card-head' }, h('h2', null, 'Player IDs')),
        h('p', { class: 'text-2 small' }, 'Valheim identifies players by an ID such as ', h('span', { class: 'mono' }, 'Steam_76561198012345678'),
          '. This page fills them in for you as people join, so you can make someone an admin or ban them with one click.'),
        h('p', { class: 'text-2 small', style: { marginTop: '8px' } }, 'Players can also see their own ID in Valheim by pressing ', h('kbd', null, 'F2'), '. Changes to these lists apply the next time the server starts.'))),
      knownCard(app),
      h('div', { class: 'grid grid-3' }, listCard('admins'), listCard('banned'), listCard('permitted')));
  },
  mount(app) {
    appRef = app;
    load();
    timer = setInterval(load, 20000);
    const A = app.actions;
    A['list-add'] = (d) => {
      const input = document.querySelector(`[data-list-input="${d.kind}"]`);
      const id = input.value.trim();
      if (!id) return input.focus();
      if (d.kind === 'permitted' && !lists.permitted.length) {
        confirmDialog({ title: 'Only allow listed players?', message: 'Once anyone is on this list, everybody else is blocked from joining, even with the password.', confirm: 'Add and restrict' })
          .then((ok) => ok && saveList(d.kind, [...lists[d.kind], id]));
        return;
      }
      input.value = '';
      saveList(d.kind, [...lists[d.kind], id]);
    };
    A['list-remove'] = (d) => saveList(d.kind, lists[d.kind].filter((x) => x !== d.id));
    A['player-admin'] = (d) => {
      const has = inList('admins', d.id);
      saveList('admins', has ? lists.admins.filter((x) => x !== d.id) : [...lists.admins, d.id]);
    };
    A['player-ban'] = async (d) => {
      const has = inList('banned', d.id);
      if (!has && !await confirmDialog({ title: `Ban ${d.name}?`, message: `${d.name} won’t be able to join once the server restarts. If they’re online now, an admin can kick them in the game (F5, then “kick ${d.name}”).`, confirm: 'Ban', kind: 'danger', icon: 'ban' })) return;
      saveList('banned', has ? lists.banned.filter((x) => x !== d.id) : [...lists.banned, d.id]);
    };
    document.addEventListener('keydown', onEnter);
  },
  unmount() {
    clearInterval(timer);
    document.removeEventListener('keydown', onEnter);
  },
};

function onEnter(e) {
  const kind = e.target && e.target.getAttribute && e.target.getAttribute('data-list-input');
  if (e.key === 'Enter' && kind) {
    e.preventDefault();
    appRef.actions['list-add']({ kind });
  }
}
