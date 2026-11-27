// Valheim's Players tab: who's online, the records (who died the most and
// so on), everyone who has played, and the admin / banned / allowed lists.
//
// Moved from the old panel's Players page (players.js), with the same
// texts and buttons, because most of it is Valheim's own: its player IDs
// (Steam_…), its console commands (F5, F2), and Make admin / Ban on each
// player. What changed: it reads and changes one instance through the new
// routes (/api/games/{id}/players, /records, /lists), keeps what it loaded
// per instance, and takes who's online from the game's own entry.

import { h, icon, api, toast, confirmDialog, copyText, dur, ago, clock, initials } from '../../dom.js';
import { gameApi, perGame, legacyStatus, redrawer } from './common.js';

// accountNote says which account a change is for: anyone can give their
// character any name, so the account is what counts.
function accountNote(id, text) {
  const steam = /^Steam_(\d+)$/.exec(id);
  return h('div', null,
    h('p', null, text),
    h('p', { class: 'small muted' }, 'Account: ', h('code', null, id),
      steam ? [' (', h('a', { href: `https://steamcommunity.com/profiles/${steam[1]}`, target: '_blank', rel: 'noopener noreferrer' }, 'Steam profile'), ')'] : null,
      '. Anyone can give their character any name, so check this is the right person.'));
}

// What the tab loaded, per instance. drafts is what's typed in each list's
// box: the page is redrawn every couple of seconds, so the boxes show it
// from here, or a redraw would empty them.
const P = perGame(() => ({ known: null, lists: null, kinds: [], recs: null, period: 'all', drafts: {}, timer: null }));
const view = redrawer();
let shown = null; // the instance whose Players tab is open

const LIST_INFO = {
  admins: ['Admins', 'shield', 'Admins can use server commands in Valheim’s console (press F5 in the game), such as kick, ban and save.'],
  banned: ['Banned', 'ban', 'These players can’t join.'],
  permitted: ['Allowed players only', 'key', 'Leave this empty to let anyone with the password join. If you add anyone here, only these players can join.'],
};

async function load(game) {
  const s = P(game);
  try {
    const [p, l] = await Promise.all([api(gameApi(game, '/players')), api(gameApi(game, '/lists'))]);
    s.known = p.players || [];
    s.kinds = (l || []).map((x) => x.kind);
    s.lists = {};
    for (const x of l || []) s.lists[x.kind.id] = x.ids || [];
  } catch (e) {
    if (!s.known) toast(e.message, 'bad');
  }
  await loadRecords(game);
}

async function loadRecords(game) {
  const s = P(game);
  const want = s.period;
  try {
    const r = await api(gameApi(game, `/records?period=${want}`));
    if (want === s.period) s.recs = { ...r, period: want };
  } catch (e) {
    if (!s.recs) s.recs = { error: e.message };
  }
  view.render();
}

// saveList saves a list and reports whether that worked.
async function saveList(game, kind, ids) {
  const s = P(game);
  let saved = false;
  try {
    await api(gameApi(game, '/lists'), { method: 'POST', body: { kind, ids } });
    s.lists[kind] = ids;
    saved = true;
    const live = (s.kinds.find((k) => k.id === kind) || {}).live;
    toast(live ? 'Saved.' : 'Saved. Changes apply the next time the server starts.', 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  load(game);
  return saved;
}

function inList(s, kind, id) { return !!(s.lists && id && s.lists[kind] && s.lists[kind].includes(id)); }

const online = (game) => game.players || [];
const maxPlayers = (game) => game.maxPlayers || 10;

function onlineCard(game) {
  const list = online(game);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Playing now'), h('span', { class: 'hint' }, `${list.length} of ${maxPlayers(game)}`)),
    list.length ? h('div', { class: 'list' }, list.map((p) => h('div', { class: 'list-row', 'data-key': p.name },
      h('div', { class: 'avatar' }, initials(p.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, p.name),
        h('div', { class: 'sub' }, `Joined at ${clock(p.since)} · playing for ${dur((Date.now() - new Date(p.since)) / 1000)}`))))) :
      h('div', { class: 'empty' }, icon('users'), game.state === 'online' ? 'Nobody’s playing right now.' : 'The server is offline.'));
}

function playedSeconds(game, p) {
  const on = online(game).find((x) => x.name === p.name);
  return (p.playSeconds || 0) + (on ? (Date.now() - new Date(on.since)) / 1000 : 0);
}

function knownCard(game) {
  const s = P(game);
  if (!s.known) return h('section', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…'));
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Everyone who has played'), h('span', { class: 'hint' }, `${s.known.length}`)),
    s.known.length ? h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Player'), h('th', null, 'Player ID'), h('th', null, 'Last seen'),
        h('th', { class: 'num' }, 'Time played'), h('th', { class: 'num' }, 'Visits'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
      h('tbody', null, s.known.map((p) => {
        const admin = inList(s, 'admins', p.platformId);
        const banned = inList(s, 'banned', p.platformId);
        return h('tr', { 'data-key': p.name },
          h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap' } }, h('div', { class: 'avatar', style: { width: '30px', height: '30px', fontSize: '12px' } }, initials(p.name)),
            h('b', null, p.name), admin ? h('span', { class: 'badge accent' }, 'Admin') : null, banned ? h('span', { class: 'badge bad' }, 'Banned') : null)),
          h('td', null, p.platformId ? h('span', { class: 'copyable' }, h('span', { class: 'mono small' }, p.platformId),
            h('button', { class: 'btn btn-ghost btn-icon btn-sm', type: 'button', 'aria-label': 'Copy player ID', onclick: () => copyText(p.platformId, 'Player ID copied') }, icon('copy'))) : h('span', { class: 'muted' }, 'unknown')),
          h('td', null, online(game).some((x) => x.name === p.name) ? h('span', { class: 'badge good' }, 'Playing now') : ago(p.lastSeen)),
          h('td', { class: 'num' }, playedSeconds(game, p) >= 60 ? dur(playedSeconds(game, p)) : '–'),
          h('td', { class: 'num' }, p.sessions || 0),
          h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap', justifyContent: 'flex-end' } },
            h('button', { class: 'btn btn-sm', type: 'button', disabled: !p.platformId, onclick: () => toggleAdmin(game, p) },
              icon('shield'), admin ? 'Remove admin' : 'Make admin'),
            h('button', { class: `btn btn-sm${banned ? '' : ' btn-danger'}`, type: 'button', disabled: !p.platformId, onclick: () => toggleBan(game, p) },
              icon('ban'), banned ? 'Unban' : 'Ban'))));
      })))) : h('div', { class: 'empty' }, 'Players appear here after they join the server.'));
}

const PERIODS = [['today', 'Today'], ['week', 'This week'], ['all', 'All time']];

function day(t) { return new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' }); }
function times(n, one, many) { return n === 1 ? `1 ${one}` : `${n} ${many}`; }

// RECORDS gives each record its title, icon and what its value means.
const RECORDS = {
  deaths: ['Died the most', 'skull', (r) => times(r.value, 'death', 'deaths')],
  dangerous: ['Dies the most often', 'zap', (r) => `a death every ${dur(r.value)}`],
  longestLife: ['Longest life', 'heart', (r) => `${dur(r.value)} without dying`],
  quickestDeath: ['Quickest death', 'timer', (r) => `dead ${dur(r.value)} into a life`],
  playTime: ['Most time played', 'clock', (r) => `${dur(r.value)} played`],
  longestSession: ['Longest session', 'hourglass', (r) => `${dur(r.value)} in one go`],
  days: ['Most days played', 'calendar', (r) => `played on ${r.value} days`],
  firstIn: ['First to arrive', 'trophy', (r, p) => (p === 'today' ? 'first in today' : `first on ${times(r.value, 'day', 'days')}`)],
  nightOwl: ['Night owl', 'moon', (r, p) => `still on at ${clock(r.at)}` + (p === 'today' ? '' : `, ${day(r.at)}`)],
  earlyBird: ['Early bird', 'sun', (r, p) => `on at ${clock(r.at)}` + (p === 'today' ? '' : `, ${day(r.at)}`)],
};

function names(list) {
  return list.length > 1 ? `${list.slice(0, -1).join(', ')} & ${list[list.length - 1]}` : list[0];
}

function recordsCard(app, game) {
  const s = P(game);
  const st = legacyStatus(game, app);
  const head = h('div', { class: 'card-head wrap' }, icon('trophy'), h('h2', null, 'Records'),
    h('div', { class: 'segmented', role: 'group', 'aria-label': 'Records for' },
      PERIODS.map(([id, label]) => h('button', {
        class: s.period === id ? 'on' : '', type: 'button', 'aria-pressed': String(s.period === id),
        onclick: () => { s.period = id; view.render(); loadRecords(game); },
      }, label))));
  const note = h('p', { class: 'small muted', style: { marginTop: '12px' } },
    'From what the server sees: who joins, leaves and dies. Crafting, food, damage and arrows stay in each player’s own game, so they can’t be counted.',
    st && st.discord && st.discord.daily ? ' Who died the most is posted in Discord at 23:59 each day.' : null);
  const recs = s.recs;
  if (!recs) return h('section', { class: 'card', 'data-key': 'records' }, head, h('p', { class: 'muted' }, 'Loading…'));
  if (recs.error) return h('section', { class: 'card', 'data-key': 'records' }, head, h('p', { class: 'muted' }, recs.error));
  const p = recs.period;
  const tiles = (recs.records || []).filter((r) => RECORDS[r.id]).map((r) => {
    const [title, ic, what] = RECORDS[r.id];
    return h('div', { class: 'tile record', 'data-key': r.id },
      h('div', { class: 'label' }, icon(ic), title),
      h('div', { class: 'value', title: r.names.join(', ') }, r.names.length > 3 ? `${r.names.length} players tie` : names(r.names)),
      h('div', { class: 'sub' }, what(r, p)));
  });
  const when = { today: 'today', week: 'this week', all: 'yet' }[p];
  return h('section', { class: 'card', 'data-key': 'records' }, head,
    (recs.players || []).length ? [
      h('div', { class: 'tiles' }, tiles),
      h('div', { class: 'table-wrap', style: { marginTop: '16px' } }, h('table', { class: 'nowrap' },
        h('thead', null, h('tr', null, h('th', null, 'Player'), h('th', { class: 'num' }, 'Deaths'), h('th', { class: 'num' }, 'Dies every'),
          h('th', { class: 'num' }, 'Longest life'), h('th', { class: 'num' }, 'Time played'))),
        h('tbody', null, recs.players.map((x) => h('tr', { 'data-key': x.name },
          h('td', null, h('b', null, x.name)),
          h('td', { class: 'num' }, x.deaths),
          h('td', { class: 'num' }, x.deaths ? dur(x.playSeconds / x.deaths) : '–'),
          h('td', { class: 'num' }, x.longestLife >= 60 ? dur(x.longestLife) : '–'),
          h('td', { class: 'num' }, x.playSeconds >= 60 ? dur(x.playSeconds) : '–')))))),
    ] : h('div', { class: 'empty' }, icon('trophy'), `Nobody has played ${when}. Records fill in as people play: who dies the most, who lasts the longest without dying, and more.`),
    note);
}

function listCard(game, kind) {
  const s = P(game);
  const k = s.kinds.find((x) => x.id === kind) || { id: kind };
  const [title, ic, help] = LIST_INFO[kind] || [k.label || kind, 'users', k.help || ''];
  const ids = (s.lists && s.lists[kind]) || [];
  const input = h('input', {
    class: 'input', placeholder: 'e.g. Steam_76561198012345678', 'data-list-input': kind, 'aria-label': `Add to ${title}`,
    value: s.drafts[kind] || '', oninput: (e) => { s.drafts[kind] = e.target.value; },
  });
  return h('section', { class: 'card', 'data-key': 'list:' + kind },
    h('div', { class: 'card-head' }, icon(ic), h('h2', null, title)),
    h('p', { class: 'small text-2', style: { marginBottom: '12px' } }, help),
    ids.length ? h('div', { class: 'chips', style: { marginBottom: '12px' } }, ids.map((id) => h('span', { class: 'chip', 'data-key': id }, id,
      h('button', { type: 'button', 'aria-label': `Remove ${id}`, onclick: () => saveList(game, kind, s.lists[kind].filter((x) => x !== id)) }, icon('x'))))) :
      h('p', { class: 'small muted', style: { marginBottom: '12px' } }, 'Nobody yet.'),
    h('div', { class: 'input-group' }, input,
      h('button', { class: 'btn', type: 'button', onclick: () => addToList(game, kind) }, icon('plus'), 'Add')));
}

async function addToList(game, kind) {
  const s = P(game);
  const id = (s.drafts[kind] || '').trim();
  if (!id) {
    const box = document.querySelector(`.vh [data-list-input="${kind}"]`);
    if (box) box.focus();
    return;
  }
  if (!s.lists) return;
  if (kind === 'permitted' && !(s.lists.permitted || []).length &&
    !await confirmDialog({ title: 'Only allow listed players?', message: 'Once anyone is on this list, everybody else is blocked from joining, even with the password.', confirm: 'Add and restrict' })) return;
  // The box is only emptied once the ID is saved, so a failed save
  // doesn't lose it.
  if (await saveList(game, kind, [...(s.lists[kind] || []), id]) && (s.drafts[kind] || '').trim() === id) {
    s.drafts[kind] = '';
    const box = document.querySelector(`.vh [data-list-input="${kind}"]`);
    if (box) box.value = ''; // a redraw leaves the box being typed in alone
    view.render();
  }
}

async function toggleAdmin(game, p) {
  const s = P(game);
  const has = inList(s, 'admins', p.platformId);
  if (!has && !await confirmDialog({ title: `Make ${p.name} an admin?`, message: accountNote(p.platformId, `${p.name} will be able to use server commands such as kick and ban once the server restarts.`), confirm: 'Make admin', icon: 'shield' })) return;
  const list = s.lists.admins || [];
  saveList(game, 'admins', has ? list.filter((x) => x !== p.platformId) : [...list, p.platformId]);
}

async function toggleBan(game, p) {
  const s = P(game);
  const has = inList(s, 'banned', p.platformId);
  if (!has && !await confirmDialog({ title: `Ban ${p.name}?`, message: accountNote(p.platformId, `${p.name} won’t be able to join once the server restarts. If they’re online now, an admin can kick them in the game (F5, then “kick ${p.name}”).`), confirm: 'Ban', kind: 'danger', icon: 'ban' })) return;
  const list = s.lists.banned || [];
  saveList(game, 'banned', has ? list.filter((x) => x !== p.platformId) : [...list, p.platformId]);
}

function onEnter(e) {
  const kind = e.target && e.target.getAttribute && e.target.getAttribute('data-list-input');
  if (e.key === 'Enter' && kind && shown && e.target.closest('.vh')) {
    e.preventDefault();
    addToList(shown, kind);
  }
}

export const playersTab = {
  title: 'Players',
  render(app, game) {
    view.set(app);
    shown = game;
    const s = P(game);
    const kinds = s.kinds.length ? s.kinds.map((k) => k.id) : Object.keys(LIST_INFO);
    return h('div', { class: 'vh stack' },
      h('div', { class: 'grid grid-2' }, onlineCard(game), h('section', { class: 'card' },
        h('div', { class: 'card-head' }, h('h2', null, 'Player IDs')),
        h('p', { class: 'text-2 small' }, 'Valheim identifies players by an ID such as ', h('span', { class: 'mono' }, 'Steam_76561198012345678'),
          '. This page fills them in for you as people join, so you can make someone an admin or ban them with one click.'),
        h('p', { class: 'text-2 small', style: { marginTop: '8px' } }, 'Players can also see their own ID in Valheim by pressing ', h('kbd', null, 'F2'), '. Changes to these lists apply the next time the server starts.'))),
      recordsCard(app, game),
      knownCard(game),
      h('div', { class: 'grid grid-3' }, kinds.map((k) => listCard(game, k))));
  },
  mount(app, game) {
    view.set(app);
    shown = game;
    const s = P(game);
    load(game);
    clearInterval(s.timer);
    s.timer = setInterval(() => load(game), 20000);
    document.addEventListener('keydown', onEnter);
  },
  unmount(app, game) {
    clearInterval(P(game).timer);
    document.removeEventListener('keydown', onEnter);
    shown = null;
  },
};
