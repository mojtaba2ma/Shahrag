/* Shahrag charts — interactive line charts drawn on canvas.
   Usage: ShahragCharts.line(canvas, items, opts)
   items: [{ts, total}] or [number] (ts = unix seconds, total = value)
   opts:  { color, key:"total"|"active", timeFormat: "HH:MM" }
   Interaction: tap/click (or hover on desktop) highlights the nearest
   point and shows a tooltip with the value and its time. */
window.ShahragCharts = (function () {
  "use strict";

  function fmtTime(ts) {
    if (!ts) return "";
    const d = new Date(ts * 1000);
    const p = n => String(n).padStart(2, "0");
    return p(d.getHours()) + ":" + p(d.getMinutes());
  }
  function fmtNum(n) {
    if (n == null) return "0";
    if (n >= 1000000) return (n / 1000000).toFixed(1) + "M";
    if (n >= 1000) return (n / 1000).toFixed(1) + "k";
    return String(Math.round(n));
  }

  function rounded(ctx, x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
  }

  function draw(cv, items, opts) {
    const color = opts.color || "#7c9eff";
    const key = opts.key || "total";
    const data = items.map(it => (typeof it === "number" ? it : (it && it[key]) || 0));
    const ts = items.map(it => (typeof it === "number" ? null : it && it.ts));

    const dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth || 320;
    const h = cv.clientHeight || 160;
    cv.width = Math.round(w * dpr);
    cv.height = Math.round(h * dpr);
    const ctx = cv.getContext("2d");
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    const css = getComputedStyle(document.documentElement);
    const textDim = css.getPropertyValue("--text-dim").trim() || "#8b93a7";
    const gridCol = css.getPropertyValue("--border").trim() || "rgba(128,140,170,0.14)";

    const padL = 44, padR = 12, padT = 12, padB = 20;
    const iw = w - padL - padR, ih = h - padT - padB;
    const max = Math.max.apply(null, data.concat([1]));
    const n = data.length;

    // Grid + y labels
    ctx.font = "10px ui-monospace, monospace";
    ctx.textAlign = "right";
    ctx.textBaseline = "middle";
    const steps = 4;
    for (let s = 0; s <= steps; s++) {
      const y = padT + (ih * s) / steps;
      ctx.strokeStyle = gridCol;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(padL, y + 0.5);
      ctx.lineTo(w - padR, y + 0.5);
      ctx.stroke();
      const val = max * (1 - s / steps);
      ctx.fillStyle = textDim;
      ctx.fillText(fmtNum(val), padL - 6, y);
    }
    // x labels: first + last time
    ctx.textAlign = "left";
    if (ts[0]) { ctx.fillStyle = textDim; ctx.fillText(fmtTime(ts[0]), padL, h - 8); }
    if (ts[n - 1] && ts[n - 1] !== ts[0]) {
      ctx.textAlign = "right";
      ctx.fillStyle = textDim;
      ctx.fillText(fmtTime(ts[n - 1]), w - padR, h - 8);
    }

    if (n === 0) return;
    const xs = n === 1 ? [padL + iw / 2] : data.map((_, i) => padL + (iw * i) / (n - 1));
    const ys = data.map(v => padT + ih - (ih * v) / max);

    // Area fill — globalAlpha with the raw color string works for
    // oklch()/hex/rgb alike (appending alpha digits only works for hex,
    // which used to paint the whole chart black).
    ctx.globalAlpha = 0.16;
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.moveTo(xs[0], padT + ih);
    xs.forEach((x, i) => ctx.lineTo(x, ys[i]));
    ctx.lineTo(xs[n - 1], padT + ih);
    ctx.closePath();
    ctx.fill();
    ctx.globalAlpha = 1;

    // Line
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.lineJoin = "round";
    ctx.lineCap = "round";
    ctx.beginPath();
    xs.forEach((x, i) => (i ? ctx.lineTo(x, ys[i]) : ctx.moveTo(x, ys[i])));
    ctx.stroke();

    // Highlighted point + tooltip
    const hi = cv._shahragHi;
    if (hi != null && hi >= 0 && hi < n) {
      const x = xs[hi], y = ys[hi];
      // vertical guide
      ctx.strokeStyle = color;
      ctx.globalAlpha = 0.35;
      ctx.setLineDash([3, 3]);
      ctx.beginPath();
      ctx.moveTo(x, padT);
      ctx.lineTo(x, padT + ih);
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.globalAlpha = 1;
      // point
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.arc(x, y, 4, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = "#ffffff";
      ctx.beginPath();
      ctx.arc(x, y, 1.8, 0, Math.PI * 2);
      ctx.fill();

      // tooltip
      const label = fmtNum(data[hi]) + (ts[hi] ? "  ·  " + fmtTime(ts[hi]) : "");
      ctx.font = "600 11px ui-sans-serif, system-ui, sans-serif";
      const tw = ctx.measureText(label).width + 16;
      const th = 24;
      let tx = x + 10;
      if (tx + tw > w - 4) tx = x - tw - 10;
      let ty = y - th - 10;
      if (ty < 2) ty = y + 10;
      ctx.fillStyle = "rgba(13, 17, 26, 0.95)";
      rounded(ctx, tx, ty, tw, th, 7);
      ctx.fill();
      ctx.strokeStyle = color;
      ctx.lineWidth = 1;
      ctx.stroke();
      ctx.fillStyle = "#e8ecf5";
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      ctx.fillText(label, tx + 8, ty + th / 2 + 0.5);
    }
  }

  function nearest(items, offsetX) {
    const n = items.length;
    if (!n) return -1;
    if (n === 1) return 0;
    const step = 1 / (n - 1);
    let idx = Math.round(offsetX / step);
    return Math.max(0, Math.min(n - 1, idx));
  }

  function line(canvas, items, opts) {
    if (!canvas || !opts) return;
    const cv = canvas;
    cv._shahragItems = items || [];
    cv._shahragOpts = opts;
    cv._shahragHi = null;
    cv.style.cursor = "crosshair";
    cv.style.touchAction = "manipulation";
    const onMove = e => {
      const r = cv.getBoundingClientRect();
      const off = e.clientX - r.left;
      const padL = 44;
      const iw = r.width - padL - 12;
      const rel = Math.max(0, Math.min(1, (off - padL) / iw));
      cv._shahragHi = nearest(items || [], rel);
      redraw();
    };
    const onLeave = () => {
      cv._shahragHi = null;
      redraw();
    };
    const redraw = () => {
      if (items && items.length >= 1) draw(cv, items, opts);
      else drawEmpty(cv, opts);
    };

    // Remove previous listeners before re-attaching (stats page reloads
    // the same canvas on every range change).
    if (cv._shahragCleanup) cv._shahragCleanup();
    cv.addEventListener("mousemove", onMove);
    cv.addEventListener("mouseleave", onLeave);
    cv.addEventListener("pointerdown", onMove);
    cv.addEventListener("touchstart", onMove, { passive: true });
    cv.addEventListener("touchend", onLeave, { passive: true });
    cv._shahragCleanup = () => {
      cv.removeEventListener("mousemove", onMove);
      cv.removeEventListener("mouseleave", onLeave);
      cv.removeEventListener("pointerdown", onMove);
      cv.removeEventListener("touchstart", onMove);
      cv.removeEventListener("touchend", onLeave);
    };

    redraw();
  }

  // Axes-only render used when there is no data yet (interaction stays
  // active so the first real data point is clickable).
  function drawEmpty(cv, opts) {
    const dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth || 320, h = cv.clientHeight || 160;
    cv.width = Math.round(w * dpr);
    cv.height = Math.round(h * dpr);
    const ctx = cv.getContext("2d");
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);
    const css = getComputedStyle(document.documentElement);
    const gridCol = css.getPropertyValue("--border").trim() || "rgba(128,140,170,0.14)";
    const textDim = css.getPropertyValue("--text-dim").trim() || "#8b93a7";
    ctx.strokeStyle = gridCol;
    ctx.lineWidth = 1;
    for (let s = 0; s <= 4; s++) {
      const y = 12 + ((h - 32) * s) / 4;
      ctx.beginPath();
      ctx.moveTo(44, y + 0.5);
      ctx.lineTo(w - 12, y + 0.5);
      ctx.stroke();
    }
    ctx.font = "11px ui-sans-serif, system-ui, sans-serif";
    ctx.fillStyle = textDim;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText("—", 44 + (w - 56) / 2, h / 2);
  }

  return { line };
})();
