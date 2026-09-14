package templates

// Templates compiled into the binary.
//
// Why any at all, when there is a repository?
// ───────────────────────────────────────────
// Because the repository is on GitHub, and the people this panel is written
// for frequently cannot reach GitHub. A real-site feature that only works
// when the network cooperates is a real-site feature that does not work. So
// five complete websites and two error-page sets ship inside the binary and
// need nothing but the binary.
//
// Why only 168 KB for seven of them
// ─────────────────────────────────
// The first cut inlined the stylesheet into every page and came to 506 KB of
// embedded assets — most of it the same CSS repeated across eleven error
// pages per template. Moving the stylesheet to assets/site.css and linking it
// took the same seven templates to 168 KB, measured, a 3.0x reduction for no
// loss of function: the pages are still self-contained, still load no
// external resource, and now share one cacheable file at run time too.

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:builtin
var builtinFS embed.FS

// builtinMetas describes each embedded template. Kept here rather than in a
// JSON file inside the embed so a typo is a compile error.
var builtinMetas = []Meta{
	{
		ID:       "corporate-slate",
		Kind:     KindSite,
		Category: "business",
		Name: Localised{"en": "Corporate Slate", "fa": "شرکتی اسلیت",
			"ar": "سليت للشركات", "tr": "Kurumsal Slate"},
		Desc: Localised{
			"en": "A clean four-page company site: hero, services, figures, about and a contact form.",
			"fa": "یک سایت شرکتی چهارصفحه‌ای و تمیز: صفحهٔ اصلی، خدمات، آمار، دربارهٔ ما و فرم تماس.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		Pages:      []string{"index.html", "about.html", "services.html", "contact.html"},
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "shop-bright",
		Kind:     KindSite,
		Category: "shop",
		Name: Localised{"en": "Shop Bright", "fa": "فروشگاهی روشن",
			"ar": "متجر مشرق", "tr": "Parlak Mağaza"},
		Desc: Localised{
			"en": "A storefront-flavoured site with delivery, returns and guarantee panels.",
			"fa": "سایتی با حال‌وهوای فروشگاه، با بخش‌های ارسال، بازگشت کالا و ضمانت.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		Pages:      []string{"index.html", "about.html", "services.html", "contact.html"},
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "clinic-calm",
		Kind:     KindSite,
		Category: "medical",
		Name: Localised{"en": "Clinic Calm", "fa": "کلینیک آرام",
			"ar": "عيادة هادئة", "tr": "Sakin Klinik"},
		Desc: Localised{
			"en": "A quiet clinic site: specialities, opening hours and an appointment form.",
			"fa": "سایت آرام یک کلینیک: تخصص‌ها، ساعات کاری و فرم نوبت‌دهی.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		Pages:      []string{"index.html", "about.html", "services.html", "contact.html"},
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "personal-ink",
		Kind:     KindSite,
		Category: "personal",
		Name: Localised{"en": "Personal Ink", "fa": "شخصی جوهر",
			"ar": "حبر شخصي", "tr": "Kişisel Mürekkep"},
		Desc: Localised{
			"en": "A warm personal page for a writer, photographer or freelancer.",
			"fa": "یک صفحهٔ شخصی گرم برای نویسنده، عکاس یا فریلنسر.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		Pages:      []string{"index.html", "about.html", "services.html", "contact.html"},
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "tech-night",
		Kind:     KindSite,
		Category: "technology",
		Name: Localised{"en": "Tech Night", "fa": "تکنولوژی شب",
			"ar": "ليل التقنية", "tr": "Teknoloji Gecesi"},
		Desc: Localised{
			"en": "A dark developer-product site with code, cloud and performance panels.",
			"fa": "سایت تیرهٔ یک محصول فنی، با بخش‌های کد، ابر و کارایی.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		Pages:      []string{"index.html", "about.html", "services.html", "contact.html"},
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "errors-plain",
		Kind:     KindError,
		Category: "errors",
		Name: Localised{"en": "Error Pages — Plain", "fa": "صفحات خطا — ساده",
			"ar": "صفحات الخطأ — بسيطة", "tr": "Hata Sayfaları — Sade"},
		Desc: Localised{
			"en": "A light, neutral set of error pages for every status nginx can return.",
			"fa": "مجموعه‌ای روشن و خنثی از صفحات خطا برای همهٔ کدهایی که nginx برمی‌گرداند.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		ErrorPages: builtinErrorCodes,
	},
	{
		ID:       "errors-dark",
		Kind:     KindError,
		Category: "errors",
		Name: Localised{"en": "Error Pages — Dark", "fa": "صفحات خطا — تیره",
			"ar": "صفحات الخطأ — داكنة", "tr": "Hata Sayfaları — Koyu"},
		Desc: Localised{
			"en": "The same set in a dark palette, to match a dark site.",
			"fa": "همان مجموعه با پالت تیره، برای هماهنگی با یک سایت تیره.",
		},
		Version: "1.0.0", License: "MIT", Author: "Shahrag",
		ErrorPages: builtinErrorCodes,
	},
}

// builtinErrorCodes is every status the embedded templates supply a page for.
// It is also the list the panel offers in the error-page editor, because a
// status with no page anywhere would be an empty dropdown entry.
var builtinErrorCodes = []string{
	"400", "401", "403", "404", "405", "413", "429", "500", "502", "503", "504",
}

// ErrorCodes returns the supported status codes.
func ErrorCodes() []string {
	out := make([]string, len(builtinErrorCodes))
	copy(out, builtinErrorCodes)
	return out
}

// BuiltinMetas returns a copy of the embedded catalogue with Builtin set.
func BuiltinMetas() []Meta {
	out := make([]Meta, 0, len(builtinMetas))
	for _, m := range builtinMetas {
		m.Builtin = true
		m.Size, m.Files = builtinSize(m.ID)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Files is the number of files in a built-in, filled by BuiltinMetas.
// Declared on Meta so the gallery shows the same columns either way.

func builtinByID(id string) (Meta, bool) {
	for _, m := range builtinMetas {
		if m.ID == id {
			m.Builtin = true
			m.Size, m.Files = builtinSize(id)
			return m, true
		}
	}
	return Meta{}, false
}

// IsBuiltin reports whether id names an embedded template.
func IsBuiltin(id string) bool {
	_, ok := builtinByID(id)
	return ok
}

func builtinSize(id string) (int64, int) {
	var total int64
	var n int
	root := "builtin/" + id
	_ = fs.WalkDir(builtinFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
			n++
		}
		return nil
	})
	return total, n
}

// BuiltinFS returns a filesystem rooted at one built-in template, used to
// preview a page without writing anything to disk.
func BuiltinFS(id string) (fs.FS, bool) {
	if !IsBuiltin(id) {
		return nil, false
	}
	sub, err := fs.Sub(builtinFS, "builtin/"+id)
	if err != nil {
		return nil, false
	}
	return sub, true
}

func copyBuiltin(m Meta, dst string) error {
	root := "builtin/" + m.ID
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(builtinFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if rel == "" {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, rerr := builtinFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// OpenTemplate returns a filesystem for any available template, built-in or
// installed, so callers do not care which it is.
func (c *Client) OpenTemplate(id string) (fs.FS, bool) {
	if f, ok := BuiltinFS(id); ok {
		return f, true
	}
	if !ValidID(id) {
		return nil, false
	}
	dir := c.templateDir(id)
	if _, err := os.Stat(filepath.Join(dir, metaFile)); err != nil {
		return nil, false
	}
	return os.DirFS(dir), true
}
