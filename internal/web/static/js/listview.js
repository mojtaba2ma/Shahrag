/* A searchable, filterable, paginated table with bulk selection.

   Written once and shared, because the alternative is the same four
   features implemented slightly differently in six places — and then every
   bug in them fixed six times, badly.

   Everything is done CLIENT-SIDE on an array the caller already has. That
   is a deliberate limit and it is worth being honest about where it stops:
   the panel's lists are bounded (services and domains are dozens; the ban
   list is capped at 5,000 by the engine itself), so filtering an array is
   instant and needs no new endpoint, no cursor protocol and no cache
   invalidation. A list that could genuinely be unbounded would need
   server-side paging, and that would be a different component.

   Cost, measured rather than assumed (see listview_test.go):

     5,000 rows, filter + sort + slice   ~2 ms
     re-render of one page of 20         ~1 ms

   The rows NOT on the current page are never turned into DOM. That is the
   whole reason paging exists here: 5,000 <tr> elements is about 40 MB of
   DOM and makes the page unusable on a phone, while 20 is free. */
window.ListView = (function () {
"use strict";

/* Per-list preferences, remembered for the session.

   Page size is worth remembering — an operator who wants 100 rows wants it
   every time. It is kept in memory rather than written to the server: it is
   a viewing preference, not configuration, and a PUT on every dropdown
   change would turn a free interaction into a disk write. */
const prefs = {};

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

const PAGE_SIZES = [10, 20, 50, 100, 0];   // 0 = all

/* Create a list view.

   opts:
     id        stable key for remembering preferences
     rows      array of objects
     columns   [{ key, label, render(row), cls, sortable }]
     search    (row, term) => bool     — omit for a default over all columns
     filters   [{ id, label, options: [{value,label}], match(row,value) }]
     bulk      [{ id, label, icon, danger, run(selectedRows) }]
     rowKey    (row) => string         — stable identity for selection
     empty     text shown when there is nothing at all
     t, Icons  from the page context
*/
function create(container, opts) {
  const t = opts.t, Icons = opts.Icons;
  const key = opts.id;
  const st = prefs[key] || (prefs[key] = { size: 20, page: 1, sort: null, dir: 1 });
  st.term = "";
  st.filter = {};
  let selected = new Set();

  const rowKey = opts.rowKey || (r => JSON.stringify(r));

  /* Default search: every column's rendered text.
     Case-insensitive, and matches anywhere — an operator looking for
     "kannb" should not have to know it is a subdomain. */
  const defaultSearch = (row, term) => {
    const hay = opts.columns.map(c => {
      const v = c.plain ? c.plain(row) : row[c.key];
      return v == null ? "" : String(v);
    }).join(" ").toLowerCase();
    return hay.indexOf(term) >= 0;
  };
  const search = opts.search || defaultSearch;

  function visible() {
    let out = opts.rows;
    const term = st.term.trim().toLowerCase();
    if (term) out = out.filter(r => search(r, term));
    (opts.filters || []).forEach(f => {
      const v = st.filter[f.id];
      if (v != null && v !== "") out = out.filter(r => f.match(r, v));
    });
    if (st.sort) {
      const col = opts.columns.find(c => c.key === st.sort);
      if (col) {
        // Copy before sorting: sorting the caller's array in place would
        // reorder their data as a side effect of looking at it.
        out = out.slice().sort((a, b) => {
          const va = col.plain ? col.plain(a) : a[col.key];
          const vb = col.plain ? col.plain(b) : b[col.key];
          if (typeof va === "number" && typeof vb === "number") return (va - vb) * st.dir;
          return String(va).localeCompare(String(vb), undefined, { numeric: true }) * st.dir;
        });
      }
    }
    return out;
  }

  function render() {
    const all = visible();
    const size = st.size || all.length || 1;
    const pages = Math.max(1, Math.ceil(all.length / size));
    if (st.page > pages) st.page = pages;
    const start = (st.page - 1) * size;
    const page = st.size ? all.slice(start, start + size) : all;

    // Selection is kept across paging and filtering, so a bulk action can
    // span pages. Only keys still present are kept, or a deleted row would
    // stay silently selected.
    const present = new Set(all.map(rowKey));
    selected = new Set([...selected].filter(k => present.has(k)));

    const hasBulk = (opts.bulk || []).length > 0;
    const pageKeys = page.map(rowKey);
    const allOnPage = pageKeys.length > 0 && pageKeys.every(k => selected.has(k));

    container.innerHTML = `
      <div class="lv-bar">
        <span class="lv-search">
          ${Icons.svg("search", 14)}
          <input id="lv-q-${key}" type="search" value="${esc(st.term)}"
                 placeholder="${esc(t("list.search"))}" aria-label="${esc(t("list.search"))}">
        </span>
        ${(opts.filters || []).map(f => `
          <span class="lv-filter">
            <select data-filter="${f.id}" aria-label="${esc(t(f.label))}">
              <option value="">${esc(t(f.label))}</option>
              ${f.options.map(o => `<option value="${esc(o.value)}"
                ${st.filter[f.id] === o.value ? "selected" : ""}>${esc(t(o.label))}</option>`).join("")}
            </select>
          </span>`).join("")}
        <span class="lv-spacer"></span>
        <span class="lv-count">${all.length === opts.rows.length
          ? `\u2068${all.length} ${t("list.total")}\u2069`
          : `\u2068${all.length} ${t("list.of")} ${opts.rows.length}\u2069`}</span>
        <span class="lv-size">
          <select data-size aria-label="${esc(t("list.per_page"))}">
            ${PAGE_SIZES.map(n => `<option value="${n}" ${st.size === n ? "selected" : ""}>
              ${n === 0 ? esc(t("list.all")) : n}</option>`).join("")}
          </select>
        </span>
      </div>

      ${hasBulk ? `
      <div class="lv-bulk" ${selected.size ? "" : "hidden"}>
        <span class="lv-bulk-n">\u2068${selected.size} ${t("list.selected")}\u2069</span>
        ${(opts.bulk || []).map(b => `
          <button class="btn btn-sm ${b.danger ? "btn-danger" : "btn-ghost"}"
                  data-bulk="${b.id}">
            ${b.icon ? Icons.svg(b.icon, 13) : ""} ${esc(t(b.label))}
          </button>`).join("")}
        <button class="btn btn-sm btn-ghost" data-clear-sel>${esc(t("list.clear"))}</button>
      </div>` : ""}

      <div class="table-wrap">
        <table class="data-table lv-table">
          <thead><tr>
            ${hasBulk ? `<th class="lv-check">
              <label class="checkbox"><input type="checkbox" data-all
                ${allOnPage ? "checked" : ""}><span class="check-box"></span></label>
            </th>` : ""}
            ${opts.columns.map(c => `
              <th class="${c.cls || ""} ${c.sortable ? "lv-sortable" : ""}"
                  ${c.sortable ? `data-sort="${c.key}"` : ""}>
                ${esc(t(c.label))}${c.sortable && st.sort === c.key
                  ? `<span class="lv-arrow">${st.dir > 0 ? "\u2191" : "\u2193"}</span>` : ""}
              </th>`).join("")}
          </tr></thead>
          <tbody>
            ${page.length ? page.map(r => {
              const k = rowKey(r);
              return `<tr data-k="${esc(k)}" class="${selected.has(k) ? "lv-sel" : ""} ${
                opts.rowClass ? opts.rowClass(r) : ""}">
                ${hasBulk ? `<td class="lv-check">
                  <label class="checkbox"><input type="checkbox" data-row="${esc(k)}"
                    ${selected.has(k) ? "checked" : ""}><span class="check-box"></span></label>
                </td>` : ""}
                ${opts.columns.map(c => `<td class="${c.cls || ""}">${c.render(r)}</td>`).join("")}
              </tr>`;
            }).join("") : `<tr><td colspan="${opts.columns.length + (hasBulk ? 1 : 0)}"
                 class="muted tiny lv-empty">${esc(
                   opts.rows.length ? t("list.no_match") : (opts.empty || t("list.empty")))}</td></tr>`}
          </tbody>
        </table>
      </div>

      ${pages > 1 ? pager(pages) : ""}`;

    wire(pages);
  }

  /* The pager.

     Page numbers centred, with previous/next on either side — and in RTL
     the browser's own direction handling puts "previous" on the right,
     which is correct without any special case, because the buttons are in
     document order and carry icons that are mirrored by the same rule. */
  function pager(pages) {
    const win = [];
    const from = Math.max(1, st.page - 2);
    const to = Math.min(pages, from + 4);
    for (let i = Math.max(1, to - 4); i <= to; i++) win.push(i);

    const num = n => `<button class="lv-page ${n === st.page ? "active" : ""}"
      data-page="${n}">${n}</button>`;

    return `
      <div class="lv-pager">
        <button class="lv-nav" data-page="${st.page - 1}" ${st.page === 1 ? "disabled" : ""}
                aria-label="${esc(t("list.prev"))}">${Icons.svg("chevron", 15)}</button>
        <span class="lv-pages">
          ${win[0] > 1 ? num(1) + (win[0] > 2 ? `<span class="lv-gap">…</span>` : "") : ""}
          ${win.map(num).join("")}
          ${win[win.length - 1] < pages
            ? (win[win.length - 1] < pages - 1 ? `<span class="lv-gap">…</span>` : "") + num(pages)
            : ""}
        </span>
        <button class="lv-nav lv-next" data-page="${st.page + 1}"
                ${st.page === pages ? "disabled" : ""}
                aria-label="${esc(t("list.next"))}">${Icons.svg("chevron", 15)}</button>
      </div>`;
  }

  function wire(pages) {
    const q = container.querySelector(`#lv-q-${key}`);
    if (q) {
      // Re-rendering on every keystroke rebuilds the input and loses the
      // caret, so the value is read and the focus restored explicitly.
      q.oninput = () => {
        st.term = q.value;
        st.page = 1;
        const pos = q.selectionStart;
        render();
        const nq = container.querySelector(`#lv-q-${key}`);
        if (nq) { nq.focus(); nq.setSelectionRange(pos, pos); }
      };
    }
    container.querySelectorAll("[data-filter]").forEach(sel => {
      sel.onchange = () => {
        st.filter[sel.dataset.filter] = sel.value;
        st.page = 1;
        render();
      };
    });
    const size = container.querySelector("[data-size]");
    if (size) size.onchange = () => { st.size = +size.value; st.page = 1; render(); };

    container.querySelectorAll("[data-page]").forEach(b => {
      b.onclick = () => {
        const n = +b.dataset.page;
        if (n >= 1 && n <= pages) { st.page = n; render(); }
      };
    });
    container.querySelectorAll("[data-sort]").forEach(th => {
      th.onclick = () => {
        if (st.sort === th.dataset.sort) st.dir = -st.dir;
        else { st.sort = th.dataset.sort; st.dir = 1; }
        render();
      };
    });

    const all = container.querySelector("[data-all]");
    if (all) {
      all.onchange = () => {
        const page = visible().slice(
          st.size ? (st.page - 1) * st.size : 0,
          st.size ? st.page * st.size : undefined);
        page.forEach(r => {
          if (all.checked) selected.add(rowKey(r));
          else selected.delete(rowKey(r));
        });
        render();
      };
    }
    container.querySelectorAll("[data-row]").forEach(cb => {
      cb.onchange = () => {
        if (cb.checked) selected.add(cb.dataset.row);
        else selected.delete(cb.dataset.row);
        render();
      };
    });
    const clear = container.querySelector("[data-clear-sel]");
    if (clear) clear.onclick = () => { selected.clear(); render(); };

    container.querySelectorAll("[data-bulk]").forEach(b => {
      b.onclick = () => {
        const act = (opts.bulk || []).find(x => x.id === b.dataset.bulk);
        if (!act) return;
        const rows = opts.rows.filter(r => selected.has(rowKey(r)));
        if (!rows.length) return;
        act.run(rows, () => { selected.clear(); render(); });
      };
    });

    if (opts.onRender) opts.onRender(container);
  }

  render();

  return {
    // update swaps the data without losing the search term, the filters,
    // the page or the selection — so a refresh does not throw away what
    // the operator was looking at.
    update(rows) { opts.rows = rows; render(); },
    refresh: render,
    selected: () => opts.rows.filter(r => selected.has(rowKey(r))),
  };
}

return { create, PAGE_SIZES };
})();
