/* A page that is a set of tabs over other pages.

   Two menu entries are built from this — Security (honeypot + auto-ban) and
   Status (health + stats + map) — and writing the machinery twice would mean
   fixing every bug in it twice. All of the awkward parts live here once:

   Lazy loading. Opening the page fetches only the tab actually shown; the
   others arrive the first time they are clicked. That keeps the cost of
   grouping pages together at zero for anyone who never opens the second tab,
   which matters because the whole point of grouping is to tidy the menu, not
   to make it slower.

   Cleanup chaining. The shell only calls content._shahragCleanup, so a pane
   that started a timer — the health page polls every 5 s, stats every 5 s —
   would keep polling for the rest of the session once the OTHER tab replaced
   it. Each pane's own cleanup is captured and run before the next one paints.

   Stale renders. A pane's render() is async. A slow tab that the user has
   already clicked away from would otherwise finish later and paint over the
   tab now on screen. Every switch takes a ticket and only the newest one is
   allowed to touch the DOM.

   navigate() translation. The panes were written as standalone pages and call
   navigate("honeypot") to redraw themselves after a save. That id is no
   longer a menu entry, so the real router would land on "page not found".
   Each pane gets a navigate that re-renders the pane when it asks for itself,
   and forwards anything else to the real router. */
window.TabPage = (function () {
"use strict";

/* Which tab was open last, per page, for this session only.

   Deliberately not persisted to the server: it is a navigation detail, not a
   setting, and writing to /api/settings/ui on every tab click would turn a
   free interaction into a disk write. */
const lastTab = {};

/* Build a tabbed page.

   id      - the page id, used for the URL hash (#status/health)
   title   - i18n key for the heading
   icon    - icon name for the heading
   tabs    - [{ id, page, icon, label }] where `page` is the module to load
             and `label` is an i18n key */
function make(opts) {
  return {
    async render(container, state, ctx) {
      const { t, Icons } = ctx;
      const tabs = opts.tabs;

      // Honour a deep link (#status/stats) so every tab keeps its own
      // address even though they share one menu entry.
      const hash = (location.hash || "").split("/")[1];
      const initial = tabs.some(x => x.id === hash)
        ? hash
        : (lastTab[opts.id] || tabs[0].id);

      container.innerHTML = `
        <div class="page-header">
          <h1>${Icons.svg(opts.icon, 20)} ${t(opts.title)}</h1>
          <span id="tp-extra"></span>
        </div>
        <div class="tabs" id="tp-tabs">
          ${tabs.map(x => `
            <button class="tab ${x.id === initial ? "active" : ""}" data-tab="${x.id}">
              ${Icons.svg(x.icon, 14)} <span>${t(x.label)}</span>
            </button>`).join("")}
        </div>
        <div id="tp-pane"></div>`;

      const pane = document.getElementById("tp-pane");

      let paneCleanup = null;
      const runCleanup = () => {
        if (typeof paneCleanup === "function") {
          try { paneCleanup(); } catch (_) {}
        }
        paneCleanup = null;
      };
      container._shahragCleanup = runCleanup;

      let ticket = 0;

      const show = async (id) => {
        const mine = ++ticket;
        runCleanup();
        lastTab[opts.id] = id;
        const tab = tabs.find(x => x.id === id) || tabs[0];

        container.querySelectorAll("#tp-tabs .tab").forEach(b =>
          b.classList.toggle("active", b.dataset.tab === id));

        // replaceState, not pushState: Back should leave the page, not
        // walk back through every tab the operator just clicked.
        try { history.replaceState(null, "", "#" + opts.id + "/" + id); } catch (_) {}

        pane.innerHTML = `<div class="empty-state"><div class="loading-spinner"></div>
          <p>${t("common.loading")}</p></div>`;

        try {
          await ctx.loadPage(tab.page);
          if (mine !== ticket) return;   // superseded while loading

          const paneCtx = Object.assign({}, ctx, {
            navigate: (to) => {
              if (to === tab.page || to === opts.id) { show(id); return; }
              ctx.navigate(to);
            },
          });
          await window.Pages[tab.page].render(pane, state, paneCtx);
          if (mine !== ticket) return;   // superseded while rendering
          paneCleanup = pane._shahragCleanup || null;
        } catch (e) {
          if (mine !== ticket) return;
          pane.innerHTML = `<div class="card"><p style="color:var(--danger);
            display:flex;gap:8px;align-items:center">
            ${Icons.svg("warning", 18)} ${e.message}</p></div>`;
        }
      };

      container.querySelectorAll("#tp-tabs .tab").forEach(b => {
        b.onclick = () => show(b.dataset.tab);
      });

      await show(initial);
    },
  };
}

return { make };
})();
