// Downloads: save a world (or a backup) as a zip. Through an IP KVM the file
// lands on the server computer, not on the viewer's own, so the panel asks
// where to save it: a USB stick, a cloud-drive folder, the Desktop…

import { h, toast } from './dom.js';

export const downloads = {}; // key -> { phase: 'packing' | 'saving', sent, total }

export function today(t = new Date()) {
  const d = new Date(t);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// saveZip downloads url, asking where to save it when the browser can (Edge
// and Chrome do), and otherwise into the Downloads folder. redraw shows the
// progress kept in downloads[key].
export async function saveZip(key, url, fileName, note, redraw) {
  if (downloads[key]) return;
  let handle = null;
  if (window.showSaveFilePicker) {
    try {
      handle = await window.showSaveFilePicker({
        suggestedName: fileName,
        types: [{ description: 'Zip file', accept: { 'application/zip': ['.zip'] } }],
      });
    } catch (e) {
      if (e.name === 'AbortError') return; // the Save window was closed
    }
  }
  if (!handle) {
    const a = h('a', { href: url, download: fileName });
    document.body.append(a);
    a.click();
    a.remove();
    toast(`Downloading “${fileName}” to the Downloads folder. Big worlds take a moment to pack.`, 'info', 8000);
    return;
  }
  downloads[key] = { phase: 'packing', sent: 0, total: 0 };
  redraw();
  let out = null;
  try {
    const res = await fetch(url, { cache: 'no-store' });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error(data.error || `The download failed (${res.status}).`);
    }
    Object.assign(downloads[key], { phase: 'saving', total: Number(res.headers.get('Content-Length')) || 0 });
    out = await handle.createWritable();
    const reader = res.body.getReader();
    let drawn = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      await out.write(value);
      downloads[key].sent += value.length;
      if (Date.now() - drawn > 200) {
        drawn = Date.now();
        redraw();
      }
    }
    await out.close();
    out = null;
    toast(`Saved “${handle.name}”.${note ? ' ' + note : ''}`, 'good', 12000);
  } catch (e) {
    if (out) await out.abort().catch(() => {});
    toast(e.message, 'bad', 10000);
  } finally {
    delete downloads[key];
    redraw();
  }
}
