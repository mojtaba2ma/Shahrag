/* Security — one menu entry holding the honeypot and automatic banning.

   Why a wrapper rather than one big merged page:

   Both features are already substantial forms — the trap has a mode, a rate,
   three exception lists and a 47-entry path preview; the ban engine has four
   independent rules plus a live table. Stacked in one scroll that is roughly
   1,400 pixels of controls before the first thing an operator came to change,
   and on a phone it is unusable. Tabs are what the Settings page already does
   for exactly this reason, so the pattern is familiar rather than novel.

   The BACKEND is untouched. Both panes call the same /api/honeypot and
   /api/autoban endpoints they always did, and both are still separately
   reachable — a bookmark, or the Telegram bot's deep link, can open
   #security/honeypot directly.

   The two pane modules are loaded LAZILY: opening this page fetches only the
   tab that is actually shown, and the other one is fetched the first time it
   is clicked. That keeps the cost of adding this menu entry at zero for
   anyone who never opens the second tab. */
window.Pages = window.Pages || {};

(function () {
"use strict";

const TABS = [
  { id: "autoban",  page: "autoban",  icon: "lock" },
  { id: "honeypot", page: "honeypot", icon: "shield" },
];

/* Which tab was open last, remembered for the session only.

   Deliberately not persisted to the server: it is a navigation detail, not a
   setting, and writing to /api/settings/ui on every tab click would turn a
   free UI interaction into a disk write. Re-opening the panel starts on the
   default tab, which is fine — that is the one most people want. */
let lastTab = "autoban";

window.Pages.security = {
  async render(container, state, ctx) {
    const { t, Icons } = ctx;

    // Honour a deep link (#security/honeypot) so both features keep their
    // own address even though they now share a menu entry.
    const hash = (location.hash || "").split("/")[1];
    let active = TABS.some(x => x.id === hash) ? hash : lastTab;

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("shield", 20)} ${t("nav.security")}</h1>
      </div>
      <div class="tabs" id="sec-tabs">
        ${TABS.map(x => `
          <button class="tab ${x.id === active ? "active" : ""}" data-tab="${x.id}">
            ${Icons.svg(x.icon, 14)} ${t("security.tab_" + x.id)}
          </button>`).join("")}
      </div>
      <div id="sec-pane"></div>`;

    const pane = document.getElementById("sec-pane");

    // Each pane keeps its own cleanup hook. The shell only calls
    // content._shahragCleanup, so a pane that started a timer would leak
    // when the OTHER tab replaced it. Chain them here.
    let paneCleanup = null;
    const runCleanup = () => {
      if (typeof paneCleanup === "function") {
        try { paneCleanup(); } catch (_) {}
      }
      paneCleanup = null;
    };
    container._shahragCleanup = runCleanup;

    const show = async (id) => {
      runCleanup();
      lastTab = id;
      const tab = TABS.find(x => x.id === id) || TABS[0];
      container.querySelectorAll("#sec-tabs .tab").forEach(b =>
        b.classList.toggle("active", b.dataset.tab === id));

      // Keep the address bar honest without adding a history entry per
      // click — replaceState, so Back leaves the Security page entirely
      // instead of walking the tabs the operator just clicked through.
      try { history.replaceState(null, "", "#security/" + id); } catch (_) {}

      pane.innerHTML = `<div class="empty-state"><div class="loading-spinner"></div>
        <p>${t("common.loading")}</p></div>`;
      try {
        await ctx.loadPage(tab.page);
        // A slow API answer plus a fast second click would otherwise let
        // the first pane paint over the second. Only render if this tab
        // is still the selected one.
        if (lastTab !== id) return;
        // The panes were written as standalone pages and call
        // navigate("honeypot") to redraw themselves after a save. That id
        // is no longer a menu entry, so navigate() would land on "page not
        // found". Give them a navigate that re-renders the pane instead —
        // which is what they actually meant — and leave any OTHER
        // destination going to the real router.
        const paneCtx = Object.assign({}, ctx, {
          navigate: (to) => {
            if (to === tab.page || to === "security") { show(id); return; }
            ctx.navigate(to);
          },
        });
        await window.Pages[tab.page].render(pane, state, paneCtx);
        paneCleanup = pane._shahragCleanup || null;
      } catch (e) {
        pane.innerHTML = `<div class="card"><p style="color:var(--danger);
          display:flex;gap:8px;align-items:center">
          ${Icons.svg("warning", 18)} ${e.message}</p></div>`;
      }
    };

    container.querySelectorAll("#sec-tabs .tab").forEach(b => {
      b.onclick = () => show(b.dataset.tab);
    });

    await show(active);
  },
};

})();
