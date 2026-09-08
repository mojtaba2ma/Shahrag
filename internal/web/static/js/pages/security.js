/* Security — one menu entry holding the honeypot and automatic banning.

   Why a wrapper rather than one big merged page: both features are already
   substantial forms — the trap has a mode, a rate, three exception lists and
   a 47-entry path preview; the ban engine has four independent rules plus a
   live table and a history. Stacked in one scroll that is roughly 1,400
   pixels of controls before the first thing an operator came to change, and
   on a phone it is unusable. Tabs are what the Settings page already does
   for exactly this reason.

   The BACKEND is untouched. Both panes call the same /api/honeypot and
   /api/autoban endpoints they always did, and both keep their own address
   (#security/honeypot) so a bookmark or a bot deep link still works.

   All the machinery lives in TabPage, shared with the Status page. */
window.Pages = window.Pages || {};
window.Pages.security = window.TabPage.make({
  id: "security",
  title: "nav.security",
  icon: "shield",
  tabs: [
    { id: "autoban",  page: "autoban",  icon: "lock",   label: "security.tab_autoban" },
    { id: "honeypot", page: "honeypot", icon: "shield", label: "security.tab_honeypot" },
  ],
});
