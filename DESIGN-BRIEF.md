# Shahrag — UI/UX Redesign Brief

Hand this file to the AI that will design the new theme. It contains everything
that AI needs to produce a drop-in replacement design.

Shahrag is an **nginx reverse-proxy control panel** written in Go. It ships as a
single binary; the whole frontend is embedded inside it.

---

## 1. What to give the designer AI

Give it **this file plus exactly two source files**:

| Give | Path | Why |
|---|---|---|
| ✅ **required** | `internal/web/templates/index.html` | the app shell it must restyle (146 lines) |
| ✅ **required** | `internal/web/static/css/app.css` | the current stylesheet it will replace (2350 lines) |

That is enough for a **complete redesign from scratch**. Everything else it needs
is described below.

**Optional, only if it wants to see real markup** (do *not* ask it to change these
— they are logic, not design):

| Optional | Path | Why |
|---|---|---|
| helpful | `internal/web/static/js/pages/services.js` | the most complex page: table + tabbed modal form |
| helpful | `internal/web/static/js/pages/settings.js` | cards, tabs, switches, backup/restore |
| helpful | `internal/web/static/js/app.js` | shows how modals/toasts/tooltips are built |
| helpful | a few screenshots of the current panel | so it knows what it is improving |

**Do NOT send:** any `.go` file, `install.sh`, `config.json`, the i18n files, or
anything containing your server's domains, ports, paths, certificates or tokens.
None of it is needed for a visual design, and some of it is sensitive.

---

## 2. Hard constraints (non-negotiable — a design that breaks these cannot ship)

1. **No build step, no framework, no CDN.** The assets are compiled *into* the Go
   binary with `go:embed` and served offline. So:
   - plain **CSS** only — no Tailwind, Sass, PostCSS, CSS-in-JS
   - plain **ES5/ES2017 JS** — no React/Vue/Svelte, no bundler, no npm
   - **no external fonts, icons or images.** No `@import url(...)`, no Google
     Fonts, no Font Awesome. Icons must be inline SVG; decorative art must be
     CSS or inline SVG. Web fonts are only acceptable as a base64 data URI, and
     even then they bloat the binary — prefer a system font stack.
2. **RTL first.** The default language is Persian and the shell runs
   `dir="rtl"`. Use CSS **logical properties** (`margin-inline-start`,
   `padding-inline`, `inset-inline-start`, `text-align: start`) instead of
   left/right. The design must also look right in LTR (English), because the
   user can switch language at runtime.
3. **Zero horizontal overflow from 320px up.** Tested at 320 / 375 / 480 / 768 /
   1024 / 1440. Any `scrollWidth > clientWidth` is a bug.
4. **Six themes driven purely by CSS variables** (see §4). The theme is switched
   by setting `<html data-theme="...">` — nothing else. All six must be
   redesigned, and text must stay legible on every surface in every theme.
5. **Keep every id, `data-*` attribute and semantic class name** (see §5). The
   JavaScript queries them. Renaming `#modal-overlay` or `.btn-primary` breaks
   the app. Purely cosmetic classes may be added freely.
6. **Assets are cached by content hash and versioned `?v=rNN`.** Nothing to do
   here — just don't add new files without telling me, since each new file must
   be registered in `index.html`.

---

## 3. What the panel must do well (the actual UX goals)

This is a **serious sysadmin tool** used to route real traffic. Aim for calm,
dense, professional — think Linear / Vercel / Grafana, not a consumer dashboard.

- **Information density over whitespace.** A user with 30 services must scan the
  table without endless scrolling.
- **Forms are the core of the product.** Most work happens inside modal forms.
  They must be tight, aligned, and readable — never a wall of grey paragraphs.
- **Explanations belong in hover tooltips**, behind a small `?` icon, not as
  permanent text under fields. (Just implemented — keep this pattern.)
- **State must be obvious at a glance:** nginx running/stopped, a service
  reachable or down, a rule that routes locally vs. passes through to the
  internet. Colour alone is not enough — pair it with a shape/label.
- **Mobile is real usage.** Admins fix servers from a phone. The sidebar
  collapses behind a hamburger; tables must degrade gracefully (the current
  design scrolls them in `.table-wrap`; a card layout per row would be better).
- **Persian typography.** Persian numerals and Latin identifiers mix constantly.
  Hostnames, ports, paths and config snippets must always render LTR + monospace
  (`dir="ltr"`, `.mono`), even inside RTL text.

---

## 4. Theme contract — CSS variables

`:root` holds the scales (spacing, radius, type, shadow, motion, layout). Six
`[data-theme="…"]` blocks then override the colour tokens. **Every colour token
below must be defined in all six themes:**

```
--bg  --bg-elev  --bg-elev2  --bg-hover  --surface
--border  --border-strong
--text  --text-dim  --text-faint
--accent  --accent-hover  --accent-soft  --accent-2
--success  --warning  --danger  --info
--chart-1 … --chart-5
--glow  --ring
```

Themes: `midnight` (default, dark blue), `aurora` (dark purple), `sunset` (dark
warm), `forest` (dark green), `light`, `high-contrast` (accessibility).

Scales already in `:root` (feel free to retune the values, keep the names):
`--sp-1…--sp-16`, `--r-1…--r-5`/`--r-full`, `--fs-xs…--fs-3xl`, `--sh-0…--sh-3`,
`--ease-out`/`--ease-in-out`, `--t-fast`/`--t-mid`/`--t-slow`,
`--sidebar-w` (248px), `--topbar-h` (56px), `--font-sans`, `--font-mono`.

Colours are currently written in `oklch()`, which is well supported and makes
consistent lightness ramps easy. Keep it if convenient.

---

## 5. Component inventory — the class/id contract

The designer must produce styles for all of these. **Names in bold are queried
by JavaScript and must not be renamed.**

### Shell
`.app-body` · `.bg-orbs`/`.orb`/`.orb-1..3` (animated background, replaceable) ·
**`#login-screen`** `.login-card` `.login-brand` `.login-title` `.login-subtitle`
`.login-footer` · **`#app-shell`** · `.sidebar` (**`#sidebar`**, gets `.open` on
mobile) `.sidebar-brand` `.brand-meta` `.brand-text` `.brand-tag` `.sidebar-nav`
(**`#sidebar-nav`**) `.nav-item` (+`.active`, has **`data-page`**)
`.sidebar-footer` `.status-pill` `.status-dot` · `.main-area` `.topbar`
`.topbar-left` `.topbar-title` `.topbar-sub` `.topbar-actions` `.content`
(**`#content`**) · **`#sidebar-toggle`**

### Layout & content
`.page-header` · `.card` `.card-title` `.card-head` `.card-grid` ·
`.stat-grid` `.stat-card` `.stat-value` `.stat-label` ·
`.table-wrap` `.data-table` `.row-main` `.row-path` `.row-actions` `.num`
`.path-line` `.path-label` · `.empty-state` `.loading-spinner` `.log-empty`
`.log-view` `.rank-list` `.resource-cell/-val/-label` ·
`.muted` `.faint` `.tiny` `.mono` `.hint`

### Controls
`.btn` + `.btn-primary` `.btn-ghost` `.btn-danger` `.btn-edit` `.btn-sm`
`.btn-lg` `.btn-block` `.btn-icon` `.btn-label` `.btn-row` · `.icon-btn` ·
`.tabs` `.tab` (+`.active`, `data-pane` / `data-kind` / `data-raw-tab`),
`.tabs.form-tabs` (two equal centred tabs inside a modal) ·
`.field` `.field-wide` `.field-row` `.field-port` `.field-icon`
`.field-icon-svg` · `input[type=text|password|number|url|email]`, `select`,
`textarea`, `.code-editor` · `.select-wrap` `.select-icon` `.select-sm`
(the select arrow is a CSS background — **it must sit on the correct side in
RTL**; getting this wrong previously made every select look unstyled) ·
`.checkbox` + `.check-box` (real `<input>` is visually hidden, `.check-box` is
the painted box) · `.switch` + `.switch-track` `.switch-thumb` ·
`.badge` + `.badge-neutral` `.badge-info` `.badge-success` `.badge-http`
(blue) `.badge-sni` (amber) · `.form-error` · `.tool` `.tool-icon`
`.file-badge`

### Overlays
**`#modal-overlay`** (hidden via the `hidden` attribute) · **`#modal`** (+`.wide`)
`.modal-header` `.modal-title` `.modal-close` (**`#modal-close-btn`**)
`.modal-body` (scrolls; `max-height` bounded) **`#modal-footer`** ·
`body.modal-open` (scroll lock, uses `position:fixed` for iOS) ·
`.toast-container` `.toast` (+`.success` `.error` `.info`, `.removing`)
`.toast-icon` ·
**`.help-tip`** (the small `?` button) and **`.tip-bubble`** (+`.visible`,
`.below`) — the bubble is appended to `<body>`, is `position:fixed`, must have
`pointer-events:none` and a z-index above the modal overlay.

### Install wizard
`.install-body` `.install-shell` `.install-header` `.brand-mark` (a separate
first-run screen; style it consistently).

---

## 6. Pages to design

| Page | Content |
|---|---|
| Dashboard | status cards, quick stats, a small topology SVG |
| **Services** | the main table: TYPE badge (HTTP/SNI), name, target, ports, bindings, row actions; add/edit modal with two tabs; a raw JSON+nginx editor modal |
| Domains | domain list + certificate paths |
| Ports | listen-port list |
| Fake site | mode selector + content editor |
| Stats | charts (hand-rolled SVG in `charts.js`), resource cells, rank lists |
| Logs | log viewer, compact icon toolbar, filters, tip callouts |
| Config files | 4 tabs of full-file editors |
| Settings | two panes (Panel / Nginx): cards, switches, backup & restore |
| Login | centred card |
| Install wizard | first-run only |

---

## 7. Deliverable I need back

Ideally a **single self-contained `app.css`** (plus an updated `index.html` if
the shell structure must change, and a note if any new inline SVG is needed).

A single static HTML mockup with inline `<style>` is also fine — I can port it.

Please state explicitly: (a) any class you renamed, (b) any markup change you
require, (c) any new asset file. Those are the only things that need code
changes on my side.

---

## 8. Current state (for context)

- Version **1.0.0**, build **r28**. Only the build tag increments.
- `app.css` is ~2350 lines and has grown in appended layers — later rules
  override earlier ones. A clean rewrite is welcome and is exactly the point.
- Recently done: field explanations moved behind `?` tooltips; SNI routing
  merged into the Services page with HTTP/SNI type badges; per-item raw
  JSON/nginx editors.
