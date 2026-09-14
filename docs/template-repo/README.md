# The Shahrag template repository

This directory is a **working copy of what the repository should look like**.
Create a new public GitHub repository — the panel's default is
`mojtaba2ma/Shahrag-Templates`, and the address is a setting so it can be
anything — and copy the contents of this directory into its root.

Nothing has to exist for the panel to work. A repository that is missing,
empty, or unreachable produces a calm "only the built-in templates are shown"
line in the gallery, and the seven templates compiled into the binary remain
fully usable. Build the repository when you want more.

---

## Layout

```
index.json                     the catalogue — the only file the panel reads first
templates/
  my-template/                 the working files (optional, for your own editing)
  my-template.tar.gz           the archive the panel downloads
assets/
  my-template/thumb.png        gallery thumbnail, 320×180 or any 16:9
  my-template/shot-1.png       optional screenshots for the details view
tools/
  pack.sh                      builds the archives and rewrites index.json
```

Only `index.json`, the `.tar.gz` files and the images are required at run
time. Keeping the unpacked sources beside them is convenient but the panel
never looks at them.

---

## How the panel fetches

Three mirrors are tried **in this order**, and the first that answers wins:

1. **Your own mirror**, if the operator set one in the panel. For someone
   inside a filtered network who keeps a copy somewhere reachable.
2. **jsDelivr** — `https://cdn.jsdelivr.net/gh/OWNER/REPO@BRANCH/PATH`.
   Tried before GitHub because on Iranian networks it is usually the one
   that resolves.
3. **raw.githubusercontent.com** — the authoritative source, frequently
   blocked.

jsDelivr serves any public GitHub repository with no registration. It caches
aggressively; after you push a change, either wait or purge it at
`https://purge.jsdelivr.net/gh/OWNER/REPO@BRANCH/index.json`.

The catalogue is cached on disk for six hours by default, and a **failure**
is remembered for five minutes — otherwise every visit to the gallery would
pay three network timeouts on a blocked link.

---

## index.json

```jsonc
{
  "schema": 1,
  "updated": "2026-09-14",

  "categories": [
    { "id": "business",   "kind": "site",  "name": { "en": "Corporate", "fa": "شرکتی" } },
    { "id": "shop",       "kind": "site",  "name": { "en": "Shop",      "fa": "فروشگاهی" } },
    { "id": "medical",    "kind": "site",  "name": { "en": "Medical",   "fa": "پزشکی" } },
    { "id": "personal",   "kind": "site",  "name": { "en": "Personal",  "fa": "شخصی" } },
    { "id": "technology", "kind": "site",  "name": { "en": "Technology","fa": "تکنولوژی" } },
    { "id": "education",  "kind": "site",  "name": { "en": "Education", "fa": "آموزشی" } },
    { "id": "restaurant", "kind": "site",  "name": { "en": "Food",      "fa": "رستوران" } },
    { "id": "errors",     "kind": "error", "name": { "en": "Error pages","fa": "صفحات خطا" } }
  ],

  "templates": [
    {
      "id": "aurora-shop",
      "kind": "site",
      "category": "shop",
      "name":        { "en": "Aurora Shop", "fa": "فروشگاه آورورا" },
      "description": { "en": "A six-page storefront with a product grid.",
                       "fa": "یک فروشگاه شش‌صفحه‌ای با شبکهٔ محصولات." },
      "version": "1.0.0",
      "author":  "you",
      "license": "MIT",
      "archive": "templates/aurora-shop.tar.gz",
      "size":    184320,
      "sha256":  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "thumb":   "assets/aurora-shop/thumb.png",
      "screenshots": ["assets/aurora-shop/shot-1.png"],
      "pages":   ["index.html", "about.html", "shop.html", "contact.html"],
      "error_pages": ["400","401","403","404","405","413","429","500","502","503","504"],
      "rtl": false
    }
  ]
}
```

### Required fields

| Field     | Why it is required |
|-----------|--------------------|
| `id`      | Lowercase letters, digits, `-` and `_`, at most 64 characters. It becomes a directory name on the server, so anything else is rejected outright. |
| `archive` | A path **inside the repository**. Absolute paths, `..` and full URLs are rejected — the panel must never be talked into fetching an arbitrary address. |
| `sha256`  | 64 hex characters. An archive whose checksum does not match is **discarded, not installed**: an unverified file from a mirror is untrusted code about to be served to your visitors. |

An entry missing any of these, or carrying an unsafe path, is silently
dropped from the list rather than shown and then failing at download time.

### Optional but worth filling in

`size` is shown before the download so somebody on a slow or metered link can
decide. `thumb` gives the gallery a real picture — without one the panel
draws a deterministic placeholder card, which is tidy but bland. `rtl: true`
marks a template designed right-to-left.

---

## Building a template

A template is a plain static website. The panel substitutes `{{NAME}}`
placeholders and copies everything else through untouched.

### Rules

* **No external requests.** No Google Fonts, no CDN, no analytics, no remote
  images. On the network this panel exists for they simply fail, and a site
  with three broken stylesheets looks worse than no site at all. Use system
  fonts, inline SVG and data URIs.
* **`index.html` at the root** (or inside a single wrapper directory — the
  panel strips one level, so both `tar czf x.tar.gz mytheme` and
  `tar czf x.tar.gz -C mytheme .` work).
* **No symlinks, no hard links, no absolute paths.** All three are refused
  when unpacking.
* **Keep it under 8 MB compressed and 32 MB unpacked**, at most 2000 files.
* Ship an `errors/` directory with `404.html`, `50x.html` and as many
  specific codes as you like. Without one the panel falls back to its
  built-in error pages, which will not match your design.

### Placeholders

Set by the panel from the operator's form. Every value is HTML-escaped.

| Placeholder | Meaning |
|---|---|
| `{{SITE_NAME}}` `{{SITE_INITIAL}}` `{{SITE_TAGLINE}}` `{{SITE_URL}}` | Identity |
| `{{LANG}}` `{{DIR}}` | `fa` / `rtl` — put them on `<html>` |
| `{{EMAIL}}` `{{PHONE}}` `{{ADDRESS}}` | Contact |
| `{{HERO_TITLE}}` `{{HERO_TEXT}}` `{{CTA_PRIMARY}}` `{{CTA_SECONDARY}}` | Above the fold |
| `{{NAV_HOME}}` `{{NAV_ABOUT}}` `{{NAV_SERVICES}}` `{{NAV_CONTACT}}` | Navigation, translated |
| `{{YEAR}}` `{{ROBOTS}}` `{{ROBOTS_RULE}}` | Footer and robots.txt |
| `{{ASSET_BASE}}` | Prefix for asset links; `/` at the root, `../` one level down. **Always use it** so a page inside `errors/` still finds the stylesheet. |
| `{{CODE}}` `{{ERROR_TITLE}}` `{{ERROR_TEXT}}` `{{E_HOME}}` `{{E_BACK}}` | Error pages only |

Anything else the operator adds under "extra placeholders" is available by
its uppercase name. **A placeholder with no value becomes an empty string**,
never the literal `{{NAME}}` — a visitor seeing braces on the page is the
clearest possible sign the site is generated.

Substitution runs on `.html .htm .css .js .txt .xml .json .svg .webmanifest`
only. Binary assets are copied byte for byte.

---

## Publishing

```bash
bash tools/pack.sh          # packs every directory under templates/ and
                            # rewrites the sha256 and size in index.json
git add -A && git commit -m "add aurora-shop" && git push
```

Then, in the panel: **Site → Templates → Check the repository**.

If the new template does not appear, jsDelivr is probably still serving the
old `index.json`. Purge it, or wait, or press the button again in a few
minutes.

---

## Checklist before publishing a template

- [ ] `grep -rn "http://\|https://" .` finds nothing but the SVG namespace
      and `{{SITE_URL}}`
- [ ] Opens correctly from `file://` with no network at all
- [ ] No horizontal scrollbar at 320 px
- [ ] `errors/404.html` and `errors/50x.html` exist and match the design
- [ ] `{{LANG}}` and `{{DIR}}` are on the `<html>` element
- [ ] Every asset link starts with `{{ASSET_BASE}}`
- [ ] The `sha256` in `index.json` matches the archive you pushed
