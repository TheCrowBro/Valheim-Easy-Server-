// Pieces several pages draw the same way: a job's progress line, a job that
// failed, notices ("callouts"), and what the rest button is called.

import { h, icon, dur } from './dom.js';

export function jobLine(job, id) {
  const pct = job.percent;
  const known = pct >= 0 && pct <= 100;
  return h('div', { class: 'jobline', role: 'status' },
    h('div', { class: 'row' }, h('b', null, job.title), h('span', { class: 'muted small' }, dur((Date.now() - new Date(job.started)) / 1000)),
      h('span', { class: 'spacer' }),
      known ? h('span', { class: 'small text-2' }, Math.round(pct) + '%') : null,
      job.cancellable ? h('button', { class: 'btn btn-sm', 'data-action': id ? 'game-cancel' : 'cancel-job', 'data-id': id || null }, 'Cancel') : null),
    job.message ? h('div', { class: 'small text-2', style: { marginTop: '4px' } }, job.message) : null,
    h('div', { class: `progress${known ? '' : ' indeterminate'}` }, h('i', { style: { width: (known ? pct : 35) + '%' } })));
}

// callout is a notice: kind is info, warn, bad or good.
export function callout(kind, ic, ...body) {
  return h('div', { class: `callout ${kind}` }, icon(ic), h('div', { class: 'body' }, ...body));
}

// What a failed job suggests doing next (Job.action), as a button for the
// game it was for.
const NEXT_STEP = {
  force_stop: ['game-force-stop', 'Force stop', 'danger'],
  install: ['game-install', 'Install the server', 'primary'],
  settings: ['go-tab', 'Open settings', '', 'settings'],
  worlds: ['go-tab', 'Open Worlds', '', 'worlds'],
  log: ['go-tab', 'Open the log', '', 'log'],
};

// failedJob is the notice for a game's job that didn't work, shown for half
// an hour unless dismissed.
export function failedJob(app, g) {
  const job = g.job;
  if (!job || !job.finished || !job.error || app.dismissed.has(job.id) || Date.now() - new Date(job.finished) > 30 * 60e3) return null;
  const next = NEXT_STEP[job.action];
  return h('div', { class: 'callout bad jobline', role: 'alert' }, icon('alert'),
    h('div', { class: 'body' }, h('p', null, h('strong', null, job.title + ' didn’t work. ')), h('p', null, job.error),
      h('div', { class: 'actions' },
        next ? h('button', { class: `btn btn-sm ${next[2] ? 'btn-' + next[2] : ''}`, 'data-action': next[0], 'data-id': g.id, 'data-tab': next[3] || null }, next[1]) : null,
        h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'dismiss-job', 'data-id': job.id }, 'Dismiss'))));
}

// restWord is what the rest button says: the computer sleeps, or (on a PC
// set up that way by an older version) hibernates.
export function restWord(st) {
  return st && st.restMode && st.restMode !== 'sleep' && st.canHibernate ? 'Hibernate' : 'Sleep';
}

// isMac reports whether the server computer is a Mac.
export const isMac = (st) => !!(st && st.platform === 'darwin');

// askNote is what the owner sees when the app asks the system for
// permission.
export const askNote = (st) => (isMac(st) ? 'macOS asks for an administrator’s password: type it and click OK, then wait a few seconds.'
  : 'Windows will ask for permission. Click Yes, then wait a few seconds.');

// loading is the card a page shows while its data loads.
export const loading = (what) => h('div', { class: 'card' }, h('p', { class: 'muted' }, what || 'Loading…'));
