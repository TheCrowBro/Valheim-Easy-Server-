// Lightweight canvas line charts for the Stats page, plus sparklines for
// the dashboard tiles. Charts on one page share a time axis and a
// crosshair: pointing at one shows the same moment on all of them.

import { h, clock } from './dom.js';

const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

// niceStep rounds a raw tick step to 1, 2, 2.5 or 5 times a power of ten.
function niceStep(raw) {
  if (!(raw > 0)) return 1;
  const exp = Math.pow(10, Math.floor(Math.log10(raw)));
  const f = raw / exp;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * exp;
}

const TIME_STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720].map((m) => m * 60000);

export class Crosshair {
  constructor() { this.charts = new Set(); this.t = null; }
  add(c) { this.charts.add(c); }
  remove(c) { this.charts.delete(c); }
  set(t, source) {
    this.t = t;
    for (const c of this.charts) c.drawHover(c === source);
  }
}

export class LineChart {
  // opts: { series: [{key, label, color, fill}], format, axisFormat, minMax, crosshair, height }
  constructor(opts) {
    this.opts = opts;
    this.data = [];
    this.t0 = 0;
    this.t1 = 1;
    this.canvas = h('canvas', { 'aria-hidden': 'true' });
    this.overlay = h('canvas', { 'aria-hidden': 'true' });
    this.tip = h('div', { class: 'chart-tip', hidden: true });
    this.el = h('div', { class: 'chart', 'data-keep': '' }, this.canvas, this.overlay, this.tip);
    if (opts.height) this.el.style.height = opts.height + 'px';
    this.crosshair = opts.crosshair;
    if (this.crosshair) this.crosshair.add(this);
    this.overlay.addEventListener('pointermove', (e) => this.onMove(e));
    this.overlay.addEventListener('pointerleave', () => this.crosshair ? this.crosshair.set(null, this) : this.drawHover(true, null));
    this.resize = new ResizeObserver(() => this.draw());
    this.resize.observe(this.el);
  }

  destroy() {
    this.resize.disconnect();
    if (this.crosshair) this.crosshair.remove(this);
  }

  setData(samples, t0, t1) {
    this.data = samples;
    this.t0 = t0;
    this.t1 = Math.max(t1, t0 + 1);
    this.draw();
  }

  value(s, ser) {
    const v = ser.get ? ser.get(s) : s[ser.key];
    return v == null || isNaN(v) ? null : v;
  }

  layout() {
    const w = this.el.clientWidth, hgt = this.el.clientHeight;
    return { w, h: hgt, l: this.left || 44, r: 14, t: 10, b: 24 };
  }

  setupCanvas(c) {
    const { w, h: hgt } = this.layout();
    const dpr = window.devicePixelRatio || 1;
    if (c.width !== Math.round(w * dpr) || c.height !== Math.round(hgt * dpr)) {
      c.width = Math.round(w * dpr);
      c.height = Math.round(hgt * dpr);
    }
    const ctx = c.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, hgt);
    return ctx;
  }

  // scales works out round axis ticks (in the chart's unit, e.g. GB) and
  // leaves room on the left for the widest tick label.
  scales() {
    let max = this.opts.minMax || 0;
    for (const s of this.data) for (const ser of this.opts.series) {
      const v = this.value(s, ser);
      if (v != null && v > max) max = v;
    }
    const unit = this.opts.unit || 1;
    let step = niceStep((max || unit) / unit / 4) * unit;
    if (this.opts.integer) step = Math.max(1, Math.round(step));
    const yMax = Math.max(step, Math.ceil(max / step - 1e-9) * step);
    const ticks = [];
    for (let v = 0; v <= yMax + step / 2; v += step) ticks.push(v);
    const fmt = this.opts.axisFormat || this.opts.format || String;
    const ctx = this.canvas.getContext('2d');
    ctx.font = `12px ${css('--font')}`;
    this.left = Math.max(36, Math.ceil(Math.max(...ticks.map((v) => ctx.measureText(fmt(v)).width))) + 14);
    const L = this.layout();
    const x = (t) => L.l + ((t - this.t0) / (this.t1 - this.t0)) * (L.w - L.l - L.r);
    const y = (v) => L.t + (1 - v / yMax) * (L.h - L.t - L.b);
    return { L, x, y, yMax, ticks };
  }

  draw() {
    if (!this.el.isConnected || this.el.clientWidth === 0) return;
    const { L, x, y, ticks } = this.scales();
    const ctx = this.setupCanvas(this.canvas);
    const fmt = this.opts.axisFormat || this.opts.format || String;
    ctx.font = `12px ${css('--font')}`;
    ctx.textBaseline = 'middle';

    // grid + y labels
    for (let i = 0; i < ticks.length; i++) {
      const v = ticks[i];
      const py = Math.round(y(v)) + 0.5;
      ctx.strokeStyle = i === 0 ? css('--axis') : css('--grid');
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(L.l, py);
      ctx.lineTo(L.w - L.r, py);
      ctx.stroke();
      ctx.fillStyle = css('--muted');
      ctx.textAlign = 'right';
      ctx.fillText(fmt(v), L.l - 8, py);
    }
    // x labels
    const span = this.t1 - this.t0;
    const step = TIME_STEPS.find((s) => span / s <= 6) || TIME_STEPS[TIME_STEPS.length - 1];
    ctx.textAlign = 'center';
    ctx.fillStyle = css('--muted');
    for (let t = Math.ceil(this.t0 / step) * step; t <= this.t1; t += step) {
      const px = x(t);
      if (px < L.l + 16 || px > L.w - L.r - 16) continue;
      ctx.fillText(clock(t), px, L.h - L.b / 2 + 2);
    }

    if (!this.data.length) {
      ctx.fillStyle = css('--muted');
      ctx.textAlign = 'center';
      ctx.fillText('No data yet', (L.l + L.w - L.r) / 2, (L.t + L.h - L.b) / 2);
      this.drawHover(false);
      return;
    }

    // a gap longer than this breaks the line (e.g. the computer was asleep)
    const gap = Math.max(10000, (span / Math.max(this.data.length, 1)) * 4);
    for (const ser of this.opts.series) {
      const runs = [];
      let run = [];
      let prevT = null;
      for (const s of this.data) {
        const v = this.value(s, ser);
        if (v == null || (prevT != null && s.t - prevT > gap)) {
          if (run.length) runs.push(run);
          run = [];
        }
        if (v != null) run.push([x(s.t), y(v)]);
        prevT = s.t;
      }
      if (run.length) runs.push(run);
      const color = ser.color;
      for (const r of runs) {
        if (ser.fill && r.length > 1) {
          ctx.beginPath();
          ctx.moveTo(r[0][0], y(0));
          for (const [px, py] of r) ctx.lineTo(px, py);
          ctx.lineTo(r[r.length - 1][0], y(0));
          ctx.closePath();
          ctx.globalAlpha = 0.1;
          ctx.fillStyle = color;
          ctx.fill();
          ctx.globalAlpha = 1;
        }
        ctx.beginPath();
        ctx.lineWidth = 2;
        ctx.lineJoin = 'round';
        ctx.lineCap = 'round';
        ctx.strokeStyle = color;
        r.forEach(([px, py], i) => (i ? ctx.lineTo(px, py) : ctx.moveTo(px, py)));
        if (r.length === 1) ctx.lineTo(r[0][0] + 0.1, r[0][1]);
        ctx.stroke();
      }
    }
    this.drawHover(false);
  }

  nearest(t) {
    const d = this.data;
    if (!d.length || t == null) return null;
    let lo = 0, hi = d.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (d[mid].t < t) lo = mid + 1; else hi = mid;
    }
    if (lo > 0 && Math.abs(d[lo - 1].t - t) < Math.abs(d[lo].t - t)) lo--;
    return d[lo];
  }

  onMove(e) {
    const rect = this.overlay.getBoundingClientRect();
    const { L } = this.scales();
    const px = Math.min(Math.max(e.clientX - rect.left, L.l), L.w - L.r);
    const t = this.t0 + ((px - L.l) / (L.w - L.l - L.r)) * (this.t1 - this.t0);
    if (this.crosshair) this.crosshair.set(t, this);
    else { this.hoverT = t; this.drawHover(true); }
  }

  drawHover(withTip) {
    if (!this.el.isConnected) return;
    const ctx = this.setupCanvas(this.overlay);
    const t = this.crosshair ? this.crosshair.t : this.hoverT;
    const s = this.nearest(t);
    if (!s || t == null) { this.tip.hidden = true; return; }
    const { L, x, y } = this.scales();
    const px = Math.round(x(s.t)) + 0.5;
    ctx.strokeStyle = css('--line-2') || 'rgba(255,255,255,.2)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(px, L.t);
    ctx.lineTo(px, L.h - L.b);
    ctx.stroke();
    const rows = [];
    for (const ser of this.opts.series) {
      const v = this.value(s, ser);
      if (v == null) continue;
      ctx.beginPath();
      ctx.arc(px, y(v), 4, 0, Math.PI * 2);
      ctx.fillStyle = ser.color;
      ctx.fill();
      ctx.lineWidth = 2;
      ctx.strokeStyle = css('--surface');
      ctx.stroke();
      rows.push(h('div', { class: 'r' }, h('i', { style: { background: ser.color } }), h('b', null, (this.opts.format || String)(v)), h('span', null, ser.label)));
    }
    if (!withTip || !rows.length) { this.tip.hidden = true; return; }
    this.tip.replaceChildren(h('div', { class: 't' }, clock(s.t)), ...rows);
    this.tip.hidden = false;
    const tw = this.tip.offsetWidth;
    let left = px + 12;
    if (left + tw > L.w) left = px - tw - 12;
    this.tip.style.left = Math.max(0, left) + 'px';
    this.tip.style.top = '6px';
  }
}

// sparkline draws a small trend line into a canvas.
export function sparkline(canvas, values, color, max) {
  const w = canvas.clientWidth, hgt = canvas.clientHeight;
  if (!w || !hgt) return;
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(hgt * dpr);
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, hgt);
  const vals = values.filter((v) => v != null);
  if (vals.length < 2) return;
  const top = Math.max(max || 0, ...vals) || 1;
  const n = values.length;
  const px = (i) => (i / (n - 1)) * (w - 2) + 1;
  const py = (v) => hgt - 2 - (v / top) * (hgt - 4);
  ctx.beginPath();
  let started = false;
  values.forEach((v, i) => {
    if (v == null) { started = false; return; }
    if (!started) { ctx.moveTo(px(i), py(v)); started = true; } else ctx.lineTo(px(i), py(v));
  });
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.5;
  ctx.lineJoin = 'round';
  ctx.stroke();
}
