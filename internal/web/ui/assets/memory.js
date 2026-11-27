// Starting a game from the panel, with the memory check (spec 6.1): the
// app checks that the game fits beside what already runs. When it doesn't,
// it answers action "memory" with what it knows, and the panel asks the
// owner what to do: stop another game and start, start anyway, or cancel.
// Each choice sends the start again with that answer.

import { h, api, toast, openModal } from './dom.js';
import { fmtMB, enc } from './state.js';

// startGame starts an instance. memory is "" (check first), "anyway" or
// "stop:<instance>". It resolves once the app has answered.
export async function startGame(app, g, memory = '') {
  try {
    await api(`/api/games/${enc(g.id)}/start`, { method: 'POST', body: { memory } });
    toast(memory.startsWith('stop:') ? `Stopping ${nameOf(app, memory.slice(5))}, then starting ${g.short}…` : `Starting ${g.short}…`, 'good');
  } catch (e) {
    if (e.data && e.data.action === 'memory') {
      memoryDialog(app, g, e);
    } else {
      toast(e.message, 'bad', 10000);
      if (e.data && e.data.fields) app.showFieldErrors(g.id, e.data.fields);
    }
  }
  app.poll();
}

function nameOf(app, id) {
  const g = ((app.status && app.status.games) || []).find((x) => x.id === id);
  return g ? g.short : id;
}

// memoryDialog is the memory warning: "Palworld needs about 10 GB of
// memory and 6.2 GB is free." with one button per running game.
export function memoryDialog(app, g, err) {
  const d = (err.data && err.data.data) || {};
  const running = (d.running || []).filter((r) => r.id !== g.id);
  const text = d.needMB
    ? `${g.short} needs about ${fmtMB(d.needMB)} of memory and ${fmtMB(d.freeMB)} is free.`
    : err.message;
  const others = running.length
    ? ` Running now: ${running.map((r) => `${r.name} (${fmtMB(r.memMB)})`).join(', ')}.`
    : '';
  openModal({
    title: `Not enough free memory for ${g.short}`,
    body: [
      h('p', null, h('strong', null, text), others),
      h('p', null, 'If it starts anyway, every game on this computer may run slowly for everyone. Stopping another game first frees its memory: its world is saved before it stops.'),
    ],
    actions: [
      ...running.map((r) => ({
        label: `Stop ${r.name} (${fmtMB(r.memMB)}) and start`,
        onClick: () => { startGame(app, g, 'stop:' + r.id); },
      })),
      { label: 'Start anyway', onClick: () => { startGame(app, g, 'anyway'); } },
      { label: 'Cancel', kind: 'primary' },
    ],
  });
}
