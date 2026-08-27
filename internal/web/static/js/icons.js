/* Shahrag icon library — custom stroke-based 24×24 SVGs */
window.Icons = (function () {
  const NS = "http://www.w3.org/2000/svg";
  const paths = {
    /* Brand: artery with ECG pulse — Shahrag (great vessel) */
    brand: '<path d="M3 12 C 6 4, 18 4, 21 12 C 18 20, 6 20, 3 12 Z" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M2 12 L6 12 L8 7 L11 17 L13 12 L16 12 L18 9 L20 13 L22 12" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',

    dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
    services:  '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1"/>',
    domains:   '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 3.8 5.8 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.8-3.8-9S9.5 5.5 12 3z"/>',
    ports:     '<path d="M4 7h16M4 12h16M4 17h16"/><circle cx="8" cy="7" r="1.6" fill="currentColor"/><circle cx="15" cy="12" r="1.6" fill="currentColor"/><circle cx="10" cy="17" r="1.6" fill="currentColor"/>',
    fakesite:  '<path d="M3 5h18v12H3z" rx="1.5"/><path d="M3 9h18M8 21h8M12 17v4"/>',
    reality:   '<path d="M12 2l2.4 5.4 5.6.7-4.2 3.9 1.2 5.8L12 15l-5 2.8 1.2-5.8L4 8.1l5.6-.7z"/>',
    stats:     '<path d="M3 3v18h18"/><path d="M7 15l3-4 3 2 4-6"/><circle cx="7" cy="15" r="1" fill="currentColor"/><circle cx="10" cy="11" r="1" fill="currentColor"/><circle cx="13" cy="13" r="1" fill="currentColor"/>',
    logs:      '<path d="M6 2h9l5 5v15H6z" rx="1.5"/><path d="M14 2v6h6M9 12h7M9 16h7M9 8h2"/>',
    settings:  '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1A1.7 1.7 0 0 0 4.6 9a1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',

    plus:      '<path d="M12 5v14M5 12h14" stroke-linecap="round"/>',
    edit:      '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
    trash:     '<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6M10 11v6M14 11v6"/>',
    close:     '<path d="M18 6L6 18M6 6l12 12" stroke-linecap="round"/>',
    check:     '<path d="M5 12l5 5L20 7" stroke-linecap="round" stroke-linejoin="round"/>',
    refresh:   '<path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.5 9a9 9 0 0 1 14.9-3.4L23 10M1 14l4.6 4.4A9 9 0 0 0 20.5 15"/>',
    logout:    '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
    menu:      '<path d="M3 6h18M3 12h18M3 18h18" stroke-linecap="round"/>',
    chevron:   '<path d="M9 6l6 6-6 6" stroke-linecap="round" stroke-linejoin="round"/>',
    search:    '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>',
    shield:    '<path d="M12 2l8 3v6c0 5-3.5 9-8 11-4.5-2-8-6-8-11V5z"/><path d="M9 12l2 2 4-4" stroke-linecap="round" stroke-linejoin="round"/>',
    globe:     '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.5 3.8 5.8 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.8-3.8-9S9.5 5.5 12 3z"/>',
    server:    '<rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01" stroke-linecap="round"/>',
    activity:  '<path d="M3 12h4l2-7 4 14 2-7h6" stroke-linecap="round" stroke-linejoin="round"/>',
    zap:       '<path d="M13 2L4 14h7l-1 8 9-12h-7z" stroke-linejoin="round"/>',
    clock:     '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2" stroke-linecap="round"/>',
    download:  '<path d="M12 3v12M7 10l5 5 5-5M5 21h14" stroke-linecap="round" stroke-linejoin="round"/>',
    upload:    '<path d="M12 21V9M7 14l5-5 5 5M5 3h14" stroke-linecap="round" stroke-linejoin="round"/>',
    copy:      '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
    key:       '<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M16 7l3 3M14 9l2 2" stroke-linecap="round"/>',
    lock:      '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
    eye:       '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>',
    eyeOff:    '<path d="M17.9 17.9A10 10 0 0 1 12 20C5 20 1 12 1 12a18 18 0 0 1 4.2-5.2M9.9 4.2A10 10 0 0 1 12 4c7 0 11 8 11 8a18 18 0 0 1-2.2 3.2M1 1l22 22" stroke-linecap="round"/><path d="M14.1 14.1a3 3 0 1 1-4.2-4.2"/>',
    cpu:       '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3" stroke-linecap="round"/>',
    database:  '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.7 4 3 9 3s9-1.3 9-3V5M3 12c0 1.7 4 3 9 3s9-1.3 9-3"/>',
    network:   '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v4M12 11.5L6 17M12 11.5L18 17"/>',
    terminal:  '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4" stroke-linecap="round" stroke-linejoin="round"/>',
    warning:   '<path d="M10.3 3.7L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.7a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01" stroke-linecap="round"/>',
    info:      '<circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01" stroke-linecap="round"/>',
    bolt:      '<path d="M13 2L4 14h7l-1 8 9-12h-7z" stroke-linejoin="round"/>',
    /* Small question mark in a circle — the hover-help affordance. */
    help:      '<circle cx="12" cy="12" r="9"/><path d="M9.2 9.3a2.9 2.9 0 1 1 3.6 2.8c-.6.2-.8.7-.8 1.3v.6" stroke-linecap="round"/><path d="M12 17.2h.01" stroke-linecap="round"/>',
  };

  function svg(name, size) {
    size = size || 18;
    const inner = paths[name] || "";
    return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${inner}</svg>`;
  }

  function brand(size, accent) {
    size = size || 28;
    const color = accent ? `style="color:${accent}"` : "";
    return `<svg class="brand-svg" width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" ${color}>${paths.brand}</svg>`;
  }

  function brandMark(size) {
    size = size || 40;
    // Two-tone brand mark with pulse
    return `
      <svg width="${size}" height="${size}" viewBox="0 0 40 40" fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <linearGradient id="bg-${size}" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stop-color="var(--accent)"/>
            <stop offset="100%" stop-color="var(--accent-2, var(--accent))"/>
          </linearGradient>
        </defs>
        <rect x="2" y="2" width="36" height="36" rx="10" fill="url(#bg-${size})" opacity="0.14"/>
        <rect x="2" y="2" width="36" height="36" rx="10" stroke="var(--accent)" stroke-width="1.4" fill="none" opacity="0.4"/>
        <path d="M6 20 C 11 9, 29 9, 34 20 C 29 31, 11 31, 6 20 Z" fill="none" stroke="var(--accent)" stroke-width="1.8" stroke-linejoin="round"/>
        <path d="M4 20 L10 20 L13 13 L17 27 L20 20 L24 20 L27 16 L30 22 L36 20" fill="none" stroke="var(--accent)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>`;
  }

  /* help(text) — a small question-mark button that reveals `text` on hover
     (or on focus / tap, so it also works without a mouse).

     Long explanatory paragraphs under a field pushed the real controls off
     the screen and made every form look noisy, so the explanation now lives
     behind this icon. The text is carried in a data attribute and rendered
     by the tooltip layer in app.js, which positions it OUTSIDE the modal —
     a tooltip drawn inside `.modal-body` would be clipped by its scroll box. */
  function help(text, size) {
    const safe = String(text == null ? "" : text)
      .replace(/&/g, "&amp;").replace(/"/g, "&quot;")
      .replace(/</g, "&lt;").replace(/>/g, "&gt;");
    return `<button type="button" class="help-tip" data-tip="${safe}"` +
      ` aria-label="${safe}" title="">${svg("help", size || 14)}</button>`;
  }

  return { svg, brand, brandMark, help, paths };
})();
