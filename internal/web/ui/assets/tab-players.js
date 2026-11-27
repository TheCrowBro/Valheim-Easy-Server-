// A game's Players tab (spec 6.1): who's on, the records (who died the
// most and so on), everyone who has played, and the game's player lists
// when it has them (admins, an allow list, bans), with Allow for anyone the
// allow list turned away.

import { h, icon, api, toast, confirmDialog, dur, ago, clock, initials } from './dom.js';
import { enc } from './state.js';

const views = new Map(); // instance -> { known, lists, recs, period, drafts }
let appRef = null;
let timer = null;
let openFor = null;

function viewFor(id) {
  let v = views.get(id);
  if (!v) views.set(id, v = { known: null, lists: null, recs: null, period: 'all', drafts: {}, error: '' });
  return v;
}

const base = (id) => `/api/games/${enc(id)}`;

async function load(id) {
  const v = viewFor(id);
  try {
    const [p, l] = await Promise.all([api(`${base(id)}/players`), api(`${base(id)}/lists`)]);
    v.known = p.players || [];
    v.lists = Array.isArray(l) ? l : [];
  } catch (e) {
    if (!v.known) v.error = e.message;
  }
  await loadRecords(id);
}

async function loadRecords(id) {
  const v = viewFor(id);
  const want = v.period;
  try {
    const r = await api(`${base(id)}/records?period=${want}`);
    if (want === v.period) v.recs = { ...r, period: want };
  } catch (e) {
    if (!v.recs) v.recs = { error: e.message };
  }
  if (appRef) appRef.render();
}

const listOf = (v, kind) => (v.lists || []).find((l) => l.kind && l.kind.id === kind);
const inList = (v, kind, pid) => { const l = listOf(v, kind); return !!(l && pid && (l.ids || []).includes(pid)); };

// saveList saves a list and reports whether that worked.
async function saveList(id, kind, ids) {
  const v = viewFor(id);
  const l = listOf(v, kind);
  let saved = false;
  try {
    await api(`${base(id)}/lists`, { method: 'POST', body: { kind, ids } });
    if (l) l.ids = ids;
    saved = true;
    toast(l && l.kind.live ? 'Saved.' : 'Saved. Changes apply the next time the server starts.', 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  }
  load(id);
  return saved;
}

function onlineCard(g) {
  const list = g.players || [];
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Playing now'), h('span', { class: 'hint' }, `${Math.max(0, g.count || list.length)} of ${g.maxPlayers || '?'}`)),
    list.length ? h('div', { class: 'list' }, list.map((p) => h('div', { class: 'list-row', 'data-key': p.name },
      h('div', { class: 'avatar' }, initials(p.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, p.name),
        h('div', { class: 'sub' }, `Joined at ${clock(p.since)} · playing for ${dur((Date.now() - new Date(p.since)) / 1000)}`))))) :
      h('div', { class: 'empty' }, icon('users'), g.state === 'online' ? (g.count > 0 ? `${g.count} playing. ${g.short} doesn’t say who.` : 'Nobody’s playing right now.') : 'The server is offline.'));
}

// turnedAway are the people the allow list kept out lately, with Allow.
function turnedAwayCard(app, g, v) {
  const allowed = listOf(v, 'allowed');
  const seen = new Set();
  const people = (app.status.activity || []).filter((a) => a.game === g.id && a.kind === 'turned_away' && a.name)
    .filter((a) => !seen.has(a.name) && seen.add(a.name) && !(allowed && (allowed.ids || []).includes(a.name)));
  if (!people.length) return null;
  return h('section', { class: 'card', 'data-key': 'turned-away' },
    h('div', { class: 'card-head' }, icon('userX'), h('h2', null, 'Turned away')),
    h('p', { class: 'small text-2', style: { marginBottom: '8px' } }, 'They tried to join but aren’t on the allow list.'),
    h('div', { class: 'list' }, people.map((a) => h('div', { class: 'list-row', 'data-key': 'ta:' + a.name },
      h('div', { class: 'avatar' }, initials(a.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, a.name), h('div', { class: 'sub' }, ago(a.time))),
      h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'allow', 'data-id': g.id, 'data-name': a.name }, icon('check'), 'Allow')))));
}

function played(g, p) {
  const online = (g.players || []).find((x) => x.name === p.name);
  return (p.playSeconds || 0) + (online ? (Date.now() - new Date(online.since)) / 1000 : 0);
}

function knownCard(g, v) {
  if (!v.known) return h('section', { class: 'card' }, h('p', { class: 'muted' }, v.error || 'Loading…'));
  // Each list can be changed from a player's row: the account a list
  // holds is the player's ID where the game has one, else their name.
  const kinds = (v.lists || []).filter((l) => l.kind && l.kind.id !== 'allowed').map((l) => l.kind);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Everyone who has played'), h('span', { class: 'hint' }, `${v.known.length}`)),
    v.known.length ? h('div', { class: 'table-wrap' }, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Player'), h('th', null, 'Last seen'),
        h('th', { class: 'num' }, 'Time played'), h('th', { class: 'num' }, 'Visits'), kinds.length ? h('th', null, h('span', { class: 'sr-only' }, 'Lists')) : null)),
      h('tbody', null, v.known.map((p) => {
        const pid = p.platformId || p.name;
        return h('tr', { 'data-key': p.name },
          h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap' } },
            h('div', { class: 'avatar', style: { width: '30px', height: '30px', fontSize: '12px' } }, initials(p.name)),
            h('div', null, h('b', null, p.name), p.platformId ? h('div', { class: 'small muted mono' }, p.platformId) : null),
            kinds.filter((k) => inList(v, k.id, pid)).map((k) => h('span', { class: `badge ${k.id === 'banned' ? 'bad' : 'accent'}` }, k.label)))),
          h('td', null, (g.players || []).some((x) => x.name === p.name) ? h('span', { class: 'badge good' }, 'Playing now') : ago(p.lastSeen)),
          h('td', { class: 'num' }, played(g, p) >= 60 ? dur(played(g, p)) : '–'),
          h('td', { class: 'num' }, p.sessions || 0),
          kinds.length ? h('td', null, h('div', { class: 'row', style: { flexWrap: 'nowrap', justifyContent: 'flex-end' } },
            kinds.map((k) => {
              const on = inList(v, k.id, pid);
              return h('button', { class: `btn btn-sm${k.id === 'banned' && !on ? ' btn-danger' : ''}`, 'data-action': 'player-list', 'data-id': g.id, 'data-kind': k.id, 'data-pid': pid, 'data-name': p.name },
                on ? `Take off ${k.label}` : `Add to ${k.label}`);
            }))) : null);
      })))) : h('div', { class: 'empty' }, 'Players appear here after they join the server.'));
}

const PERIODS = [['today', 'Today'], ['week', 'This week'], ['all', 'All time']];
const day = (t) => new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' });
const times = (n, one, many) => (n === 1 ? `1 ${one}` : `${n} ${many}`);
const DEATHS = new Set(['deaths', 'dangerous', 'longestLife', 'quickestDeath']);

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

const names = (list) => (list.length > 1 ? `${list.slice(0, -1).join(', ')} & ${list[list.length - 1]}` : list[0]);

function recordsCard(g, v) {
  const head = h('div', { class: 'card-head wrap' }, icon('trophy'), h('h2', null, 'Records'),
    h('div', { class: 'segmented', role: 'group', 'aria-label': 'Records for' },
      PERIODS.map(([id, label]) => h('button', { class: v.period === id ? 'on' : '', 'data-action': 'records-period', 'data-id': g.id, 'data-period': id, 'aria-pressed': String(v.period === id) }, label))));
  const r = v.recs;
  if (!r) return h('section', { class: 'card', 'data-key': 'records' }, head, h('p', { class: 'muted' }, 'Loading…'));
  if (r.error) return h('section', { class: 'card', 'data-key': 'records' }, head, h('p', { class: 'muted' }, r.error));
  const p = r.period;
  // Deaths are shown only for a game whose server tells the app about them.
  const deaths = (r.records || []).some((x) => DEATHS.has(x.id)) || (r.players || []).some((x) => x.deaths > 0);
  const tiles = (r.records || []).filter((x) => RECORDS[x.id]).map((x) => {
    const [title, ic, what] = RECORDS[x.id];
    return h('div', { class: 'tile record', 'data-key': x.id },
      h('div', { class: 'label' }, icon(ic), title),
      h('div', { class: 'value', title: x.names.join(', ') }, x.names.length > 3 ? `${x.names.length} players tie` : names(x.names)),
      h('div', { class: 'sub' }, what(x, p)));
  });
  const when = { today: 'today', week: 'this week', all: 'yet' }[p];
  return h('section', { class: 'card', 'data-key': 'records' }, head,
    (r.players || []).length ? [
      h('div', { class: 'tiles' }, tiles),
      h('div', { class: 'table-wrap', style: { marginTop: '16px' } }, h('table', { class: 'nowrap' },
        h('thead', null, h('tr', null, h('th', null, 'Player'),
          deaths ? [h('th', { class: 'num' }, 'Deaths'), h('th', { class: 'num' }, 'Dies every'), h('th', { class: 'num' }, 'Longest life')] : null,
          h('th', { class: 'num' }, 'Time played'), h('th', { class: 'num' }, 'Days'))),
        h('tbody', null, r.players.map((x) => h('tr', { 'data-key': x.name },
          h('td', null, h('b', null, x.name)),
          deaths ? [h('td', { class: 'num' }, x.deaths),
            h('td', { class: 'num' }, x.deaths ? dur(x.playSeconds / x.deaths) : '–'),
            h('td', { class: 'num' }, x.longestLife >= 60 ? dur(x.longestLife) : '–')] : null,
          h('td', { class: 'num' }, x.playSeconds >= 60 ? dur(x.playSeconds) : '–'),
          h('td', { class: 'num' }, x.days || 0)))))),
    ] : h('div', { class: 'empty' }, icon('trophy'), `Nobody has played ${when}. Records fill in as people play.`),
    h('p', { class: 'small muted', style: { marginTop: '12px' } }, 'From what the server tells the app: who joins and leaves, and who dies where the game says so.'));
}

function listCard(g, v, l) {
  const k = l.kind;
  const ids = l.ids || [];
  const draft = v.drafts[k.id] || '';
  const input = h('input', { class: 'input', placeholder: k.placeholder || '', 'data-list-input': `${g.id}|${k.id}`, 'aria-label': `Add to ${k.label}`,
    value: draft, oninput: (e) => { v.drafts[k.id] = e.target.value; } });
  return h('section', { class: 'card', 'data-key': 'list:' + k.id },
    h('div', { class: 'card-head' }, icon(k.id === 'banned' ? 'ban' : k.id === 'admins' ? 'shield' : 'key'), h('h2', null, k.label)),
    k.help ? h('p', { class: 'small text-2', style: { marginBottom: '12px' } }, k.help) : null,
    ids.length ? h('div', { class: 'chips', style: { marginBottom: '12px' } }, ids.map((id) => h('span', { class: 'chip', 'data-key': id }, id,
      h('button', { 'data-action': 'list-remove', 'data-id': g.id, 'data-kind': k.id, 'data-pid': id, 'aria-label': `Take ${id} off ${k.label}` }, icon('x'))))) :
      h('p', { class: 'small muted', style: { marginBottom: '12px' } }, 'Nobody yet.'),
    h('div', { class: 'input-group' }, input,
      h('button', { class: 'btn', 'data-action': 'list-add', 'data-id': g.id, 'data-kind': k.id }, icon('plus'), 'Add')));
}

export default {
  render(app, g) {
    appRef = app;
    const v = viewFor(g.id);
    const lists = v.lists || [];
    return h('div', { class: 'stack' },
      h('div', { class: 'grid grid-2' }, onlineCard(g), turnedAwayCard(app, g, v) || h('section', { class: 'card' },
        h('div', { class: 'card-head' }, h('h2', null, 'Players')),
        h('p', { class: 'text-2 small' }, `Everyone who joins ${g.short} is listed here, with how long they played.`,
          lists.length ? ' The lists below say who may join and who can use the server’s commands.' : ''))),
      recordsCard(g, v),
      knownCard(g, v),
      lists.length ? h('div', { class: `grid grid-${Math.min(3, lists.length)}` }, lists.map((l) => listCard(g, v, l))) : null);
  },
  mount(app, g) {
    appRef = app;
    openFor = g.id;
    load(g.id);
    clearInterval(timer);
    timer = setInterval(() => { if (openFor) load(openFor); }, 20000);
    const A = app.actions;
    A['records-period'] = (d) => {
      viewFor(d.id).period = d.period;
      app.render();
      loadRecords(d.id);
    };
    A['list-add'] = async (d) => {
      const v = viewFor(d.id);
      const l = listOf(v, d.kind);
      const pid = (v.drafts[d.kind] || '').trim();
      if (!l) return;
      if (!pid) {
        const el = document.querySelector(`[data-list-input="${d.id}|${d.kind}"]`);
        if (el) el.focus();
        return;
      }
      if (await saveList(d.id, d.kind, [...(l.ids || []), pid]) && (v.drafts[d.kind] || '').trim() === pid) {
        v.drafts[d.kind] = '';
        const el = document.querySelector(`[data-list-input="${d.id}|${d.kind}"]`);
        if (el) el.value = ''; // a redraw leaves the box being typed in alone
        app.render();
      }
    };
    A['list-remove'] = (d) => {
      const l = listOf(viewFor(d.id), d.kind);
      if (l) saveList(d.id, d.kind, (l.ids || []).filter((x) => x !== d.pid));
    };
    A['player-list'] = async (d) => {
      const v = viewFor(d.id);
      const l = listOf(v, d.kind);
      if (!l) return;
      const on = inList(v, d.kind, d.pid);
      if (!on && !await confirmDialog({ title: `Add ${d.name} to ${l.kind.label}?`, message: (l.kind.help || '') + (d.pid !== d.name ? ` Account: ${d.pid}.` : ''), confirm: 'Add', kind: d.kind === 'banned' ? 'danger' : 'primary' })) return;
      saveList(d.id, d.kind, on ? (l.ids || []).filter((x) => x !== d.pid) : [...(l.ids || []), d.pid]);
    };
    document.addEventListener('keydown', onEnter);
  },
  unmount() {
    clearInterval(timer);
    openFor = null;
    document.removeEventListener('keydown', onEnter);
  },
};

function onEnter(e) {
  const key = e.target && e.target.getAttribute && e.target.getAttribute('data-list-input');
  if (e.key === 'Enter' && key) {
    e.preventDefault();
    const [id, kind] = key.split('|');
    appRef.actions['list-add']({ id, kind });
  }
}
