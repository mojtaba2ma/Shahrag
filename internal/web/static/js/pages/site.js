/* Site — one menu entry holding everything a domain's public face is made of.

   Three panes, in the order an operator actually works through them:

     Real site   the per-domain switch and settings. This is where people
                 come back to, so it is first.
     Templates   the gallery. Visited once when choosing a design, then
                 rarely, so it is not the landing tab.
     Fake page   the original loading-spinner page, unchanged. It still
                 serves every domain that has no real site, which after an
                 upgrade is all of them, so it cannot be removed and must
                 not be hidden.

   The fake page and the real site are deliberately NOT merged into one
   screen. They answer the same question but they are different mechanisms
   with different blast radii: the fake page is one file shared by every
   domain, the real site is a rendered tree per domain. Presenting them as
   one form would imply changing one changes the other.

   All the machinery lives in TabPage, shared with Security and Status. */
window.Pages = window.Pages || {};
window.Pages.site = window.TabPage.make({
  id: "site",
  title: "nav.site",
  icon: "globe",
  tabs: [
    { id: "realsite",  page: "realsite",  icon: "globe",    label: "site.tab_real" },
    { id: "templates", page: "templates", icon: "fakesite", label: "site.tab_templates" },
    { id: "fakesite",  page: "fakesite",  icon: "fakesite", label: "site.tab_fake" },
  ],
});
