/* Shahrag charts — interactive line charts drawn on canvas.

   ShahragCharts.line(canvas, items, opts)
     items: [{ts, v:number}] or [number]; opts: {color, label}
   ShahragCharts.multi(canvas, items, opts)
     items: [{ts, tcp:12, udp:4}]; opts: {series:[{key,color,label}], legend:true}
   ShahragCharts.update(canvas, items)
     pushes new data into an already-configured chart (live updates).

   Interaction: tap/click (or hover) highlights the nearest point and
   shows a tooltip — date/time on top (small, dim) and the value below
   (larger, brighter). */
window.ShahragCharts = (function () {
  "use strict";

  function fmtTime(ts) {
    if (!ts) return "";
    const d = new Date(ts * 1000);
    const p = n => String(n).padStart(2, "0");
    return p(d.getHours()) + ":" + p(d.getMinutes());
  }
  function fmtDateTime(ts) {
    if (!ts) return "";
    const d = new Date(ts * 1000);
    const p = n => String(n).padStart(2, "0");
    return d.getFullYear() + "/" + p(d.getMonth() + 1) + "/" + p(d.getDate()) + "  " + p(d.getHours()) + ":" + p(d.getMinutes());
  }
  function fmtNum(n) {
    if (n == null) return "0";
    if (Math.abs(n) >= 1000000) return (n / 1000000).toFixed(1) + "M";
    if (Math.abs(n) >= 1000) return (n / 1000).toFixed(1) + "k";
    if (n % 1 !== 0) return n.toFixed(1);
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

  // series: [{key, color, label}]
  function drawMulti(cv, items, series) {
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

    const padL = 40, padR = 12, padT = 16, padB = 20;
    const iw = w - padL - padR, ih = h - padT - padB;
    const n = items.length;
    const keys = series.map(s => s.key);

    // legend (top-right)
    if (series.length > 1) {
      ctx.font = "600 10px ui-sans-serif, system-ui, sans-serif";
      ctx.textBaseline = "middle";
      let lx = w - padR;
      for (let i = series.length - 1; i >= 0; i--) {
        const s = series[i];
        const label = s.label || s.key;
        const tw = ctx.measureText(label).width;
        lx -= tw + 14;
        ctx.fillStyle = s.color;
        ctx.fillRect(lx, 6, 8, 8);
        ctx.fillStyle = textDim;
        ctx.textAlign = "left";
        ctx.fillText(label, lx + 12, 10.5);
        ctx.textAlign = "right";
        lx -= 6;
      }
    }

    // global max across series
    let max = 1;
    keys.forEach(k => items.forEach(it => { const v = it[k] || 0; if (v > max) max = v; }));

    // grid + y labels
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
      ctx.fillStyle = textDim;
      ctx.fillText(fmtNum(max * (1 - s / steps)), padL - 6, y);
    }
    // x time labels
    ctx.textAlign = "left";
    if (items[0] && items[0].ts) { ctx.fillStyle = textDim; ctx.fillText(fmtTime(items[0].ts), padL, h - 8); }
    if (items[n - 1] && items[n - 1].ts && items[n - 1].ts !== items[0].ts) {
      ctx.textAlign = "right";
      ctx.fillStyle = textDim;
      ctx.fillText(fmtTime(items[n - 1].ts), w - padR, h - 8);
    }

    if (n === 0) return;
    const xs = n === 1 ? [padL + iw / 2] : items.map((_, i) => padL + (iw * i) / (n - 1));

    series.forEach(s => {
      const vals = items.map(it => (typeof it === "number" ? it : (it && it[s.key]) || 0));
      const ys = vals.map(v => padT + ih - (ih * v) / max);
      // area
      ctx.globalAlpha = 0.12;
      ctx.fillStyle = s.color;
      ctx.beginPath();
      ctx.moveTo(xs[0], padT + ih);
      xs.forEach((x, i) => ctx.lineTo(x, ys[i]));
      ctx.lineTo(xs[n - 1], padT + ih);
      ctx.closePath();
      ctx.fill();
      ctx.globalAlpha = 1;
      // line
      ctx.strokeStyle = s.color;
      ctx.lineWidth = 2;
      ctx.lineJoin = "round";
      ctx.lineCap = "round";
      ctx.beginPath();
      xs.forEach((x, i) => (i ? ctx.lineTo(x, ys[i]) : ctx.moveTo(x, ys[i])));
      ctx.stroke();
    });

    // highlighted point (primary series = first)
    const hi = cv._shahragHi;
    if (hi != null && hi >= 0 && hi < n && n > 0) {
      const prim = series[0];
      const v0 = items[hi] && (typeof items[hi] === "number" ? items[hi] : items[hi][prim.key]) || 0;
      const y0 = padT + ih - (ih * v0) / max;
      const x0 = xs[hi];
      ctx.strokeStyle = prim.color;
      ctx.globalAlpha = 0.35;
      ctx.setLineDash([3, 3]);
      ctx.beginPath();
      ctx.moveTo(x0, padT);
      ctx.lineTo(x0, padT + ih);
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.globalAlpha = 1;
      ctx.fillStyle = prim.color;
      ctx.beginPath();
      ctx.arc(x0, y0, 4, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = "#ffffff";
      ctx.beginPath();
      ctx.arc(x0, y0, 1.8, 0, Math.PI * 2);
      ctx.fill();

      // tooltip: date/time small & dim on top, value(s) below
      const timeStr = fmtDateTime(items[hi] && items[hi].ts);
      let valueStr = fmtNum(v0);
      let subLines = [];
      if (series.length > 1) {
        const parts = series.map(s => {
          const v = items[hi] && (typeof items[hi] === "number" ? items[hi] : items[hi][s.key]) || 0;
          return (s.label || s.key) + " " + fmtNum(v);
        });
        valueStr = parts.join("   ");
      }
      ctx.font = "600 13px ui-sans-serif, system-ui, sans-serif";
      const vw = ctx.measureText(valueStr).width;
      ctx.font = "10px ui-sans-serif, system-ui, sans-serif";
      const tw = ctx.measureText(timeStr).width;
      const bw = Math.max(vw, tw) + 20;
      const bh = timeStr ? 40 : 26;
      let bx = x0 + 12;
      if (bx + bw > w - 4) bx = x0 - bw - 12;
      let by = y0 - bh - 12;
      if (by < 2) by = y0 + 12;
      ctx.fillStyle = "rgba(13, 17, 26, 0.96)";
      rounded(ctx, bx, by, bw, bh, 8);
      ctx.fill();
      ctx.strokeStyle = prim.color;
      ctx.lineWidth = 1;
      ctx.stroke();
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      if (timeStr) {
        ctx.font = "10px ui-sans-serif, system-ui, sans-serif";
        ctx.fillStyle = "rgba(200, 208, 226, 0.75)"; // dimmer than the value
        ctx.fillText(timeStr, bx + 10, by + 13);
      }
      ctx.font = "600 13px ui-sans-serif, system-ui, sans-serif";
      ctx.fillStyle = "#f2f5fb";
      ctx.fillText(valueStr, bx + 10, by + (timeStr ? 29 : 13));
      void subLines;
    }
  }

  function nearest(items, offsetX) {
    const n = items.length;
    if (!n) return -1;
    if (n === 1) return 0;
    const step = 1 / (n - 1);
    return Math.max(0, Math.min(n - 1, Math.round(offsetX / step)));
  }

  function configure(cv, items, opts) {
    // Accept both {key,color} (single series, e.g. dashboard items with
    // a "total" field) and {series:[...]} (multi-series charts).
    const series = opts.series
      || [{ key: opts.key || "v", color: opts.color || "#7c9eff", label: opts.label || "" }];
    cv._shahragItems = items || [];
    cv._shahragSeries = series;
    cv._shahragHi = null;
    cv.style.cursor = "crosshair";
    cv.style.touchAction = "manipulation";

    const redraw = () => drawMulti(cv, cv._shahragItems || [], cv._shahragSeries);
    const onMove = e => {
      const r = cv.getBoundingClientRect();
      const off = e.clientX - r.left;
      const padL = 40;
      const iw = r.width - padL - 12;
      const rel = Math.max(0, Math.min(1, (off - padL) / iw));
      cv._shahragHi = nearest(cv._shahragItems || [], rel);
      redraw();
    };
    const onLeave = () => { cv._shahragHi = null; redraw(); };

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

  function line(canvas, items, opts) {
    if (!canvas || !opts) return;
    configure(canvas, items, opts);
  }
  function multi(canvas, items, opts) {
    if (!canvas || !opts) return;
    configure(canvas, items, opts);
  }
  function update(canvas, items) {
    if (!canvas || !canvas._shahragSeries) return;
    canvas._shahragItems = items || [];
    drawMulti(canvas, canvas._shahragItems, canvas._shahragSeries);
  }

  return { line, multi, update };
})();
