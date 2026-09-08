/* Status — health, statistics and the topology map in one menu entry.

   These three answer the same question at three timescales: what is the
   server doing right now (health), what has it been doing (stats), and how
   is it wired together (map). Three separate menu entries for one question
   made the sidebar long and made the operator hunt.

   Same wrapper as Security, same lazy loading — opening Status fetches only
   the tab shown, so grouping costs nothing for anyone who only ever looks at
   one of them. */
window.Pages = window.Pages || {};
window.Pages.status = window.TabPage.make({
  id: "status",
  title: "nav.status",
  icon: "activity",
  tabs: [
    { id: "health", page: "health", icon: "activity", label: "status.tab_health" },
    { id: "stats",  page: "stats",  icon: "stats",    label: "status.tab_stats" },
    { id: "map",    page: "map",    icon: "network",  label: "status.tab_map" },
  ],
});
