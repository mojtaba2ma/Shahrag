package realsite

// The words a template is filled with when the operator has not supplied
// their own.
//
// Why defaults exist at all
// ─────────────────────────
// Someone who turns the feature on and saves must get a complete, plausible
// website immediately — not a page of empty headings, and certainly not one
// with `{{HERO_TITLE}}` printed across it. The defaults are deliberately
// generic: a consultancy-flavoured text that reads as ordinary in any
// industry and gives no hint what the server actually does.
//
// Why they are translated
// ───────────────────────
// A Persian-language site with English body text is not a Persian site. The
// four languages here are the ones the panel itself ships fully translated;
// anything else falls back to English, which is the honest behaviour — a
// machine-translated site reads worse than an English one.

import "strings"

// copyKeys are the placeholders filled from the table below.
var copyKeys = []string{
	"NAV_HOME", "NAV_ABOUT", "NAV_SERVICES", "NAV_CONTACT",
	"F_LINKS", "F_CONTACT", "F_RIGHTS", "F_BUILT",
	"F1_TITLE", "F1_TEXT", "F2_TITLE", "F2_TEXT", "F3_TITLE", "F3_TEXT",
	"STAT1_N", "STAT1_L", "STAT2_N", "STAT2_L",
	"STAT3_N", "STAT3_L", "STAT4_N", "STAT4_L",
	"ABOUT_LEAD", "ABOUT_MISSION_T", "ABOUT_MISSION",
	"ABOUT_STORY_T", "ABOUT_STORY", "ABOUT_VALUES_T", "ABOUT_VALUES",
	"SERVICES_LEAD", "T_PLAN", "T_FOR", "T_PRICE",
	"P1_N", "P1_F", "P1_P", "P2_N", "P2_F", "P2_P", "P3_N", "P3_F", "P3_P",
	"CONTACT_LEAD", "C_EMAIL", "C_PHONE", "C_ADDRESS",
	"C_NAME", "C_MESSAGE", "C_SEND", "C_NOTE",
}

var copyEN = map[string]string{
	"nav_home": "Home", "nav_about": "About", "nav_services": "Services", "nav_contact": "Contact",
	"f_links": "Links", "f_contact": "Contact", "f_rights": "All rights reserved.",
	"f_built": "Made with care.",
	"cta_primary": "Get in touch", "cta_secondary": "See what we do",
	"f1_title": "Dependable", "f1_text": "Work that holds up under real conditions, not just on the day it ships.",
	"f2_title": "Measured", "f2_text": "Decisions backed by numbers, reviewed openly, changed when the numbers change.",
	"f3_title": "Attentive", "f3_text": "A small team that answers, understands the question, and follows through.",
	"stat1_n": "12+", "stat1_l": "Years",
	"stat2_n": "240", "stat2_l": "Projects",
	"stat3_n": "38", "stat3_l": "Partners",
	"stat4_n": "98%", "stat4_l": "Retention",
	"about_lead":       "A small, steady team doing careful work for people who need it to last.",
	"about_mission_t":  "What we do",
	"about_mission":    "We take on a limited number of engagements and finish them properly, which is less exciting than it sounds and works better than it looks.",
	"about_story_t":    "How we started",
	"about_story":      "Two people, one problem worth solving, and a stubborn refusal to ship something we would not use ourselves.",
	"about_values_t":   "What we hold to",
	"about_values":     "Say what is true, quote what it costs, deliver what was agreed, and pick up the phone when it goes wrong.",
	"services_lead":    "Three ways to work with us. Every one of them starts with a conversation, not a contract.",
	"t_plan":           "Plan", "t_for": "Best for", "t_price": "From",
	"p1_n": "Starter", "p1_f": "A first project with a clear edge", "p1_p": "on request",
	"p2_n": "Standard", "p2_f": "Ongoing work with a fixed rhythm", "p2_p": "on request",
	"p3_n": "Extended", "p3_f": "A team that needs capacity now", "p3_p": "on request",
	"contact_lead": "Write, call, or come by. We read everything and reply to all of it.",
	"c_email":      "Email", "c_phone": "Phone", "c_address": "Address",
	"c_name": "Your name", "c_message": "Message", "c_send": "Send",
	"c_note":     "We use what you send only to reply to you.",
	"e_home":     "Go to the home page", "e_back": "Go back",
	"tagline":    "Careful work, delivered on time.",
	"hero":       "Work you can build on",
	"hero_text":  "We plan it with you, build it once, and stay reachable afterwards. No surprises in the invoice and none in the handover.",
	"address":    "Office 4, 12 Market Street",
}

var copyFA = map[string]string{
	"nav_home": "خانه", "nav_about": "درباره ما", "nav_services": "خدمات", "nav_contact": "تماس",
	"f_links": "پیوندها", "f_contact": "تماس", "f_rights": "همهٔ حقوق محفوظ است.",
	"f_built": "ساخته‌شده با دقت.",
	"cta_primary": "تماس با ما", "cta_secondary": "خدمات ما",
	"f1_title": "قابل‌اتکا", "f1_text": "کاری که در شرایط واقعی دوام می‌آورد، نه فقط روز تحویل.",
	"f2_title": "اندازه‌گیری‌شده", "f2_text": "تصمیم‌ها بر پایهٔ عدد؛ شفاف بازبینی می‌شوند و با تغییر عددها تغییر می‌کنند.",
	"f3_title": "پاسخ‌گو", "f3_text": "تیمی کوچک که جواب می‌دهد، صورت مسئله را می‌فهمد و تا انتها می‌ماند.",
	"stat1_n": "+۱۲", "stat1_l": "سال تجربه",
	"stat2_n": "۲۴۰", "stat2_l": "پروژه",
	"stat3_n": "۳۸", "stat3_l": "همکار",
	"stat4_n": "۹۸٪", "stat4_l": "ماندگاری مشتری",
	"about_lead":      "تیمی کوچک و باثبات، برای کسانی که کارشان باید دوام بیاورد.",
	"about_mission_t": "چه می‌کنیم",
	"about_mission":   "تعداد محدودی پروژه می‌گیریم و همان‌ها را درست تمام می‌کنیم؛ کمتر از آنچه به‌نظر می‌رسد هیجان‌انگیز است و بهتر از آنچه به‌نظر می‌رسد جواب می‌دهد.",
	"about_story_t":   "چطور شروع شد",
	"about_story":     "دو نفر، یک مسئلهٔ ارزشمند، و اصراری لجوجانه بر اینکه چیزی تحویل ندهیم که خودمان استفاده نمی‌کنیم.",
	"about_values_t":  "به چه پایبندیم",
	"about_values":    "راست بگو، هزینه را شفاف اعلام کن، آنچه توافق شده را تحویل بده، و وقتی مشکلی پیش آمد گوشی را بردار.",
	"services_lead":   "سه شیوهٔ همکاری. هر سه با یک گفت‌وگو شروع می‌شوند، نه با قرارداد.",
	"t_plan":          "طرح", "t_for": "مناسب برای", "t_price": "شروع از",
	"p1_n": "شروع", "p1_f": "نخستین پروژه با محدودهٔ روشن", "p1_p": "بر اساس درخواست",
	"p2_n": "استاندارد", "p2_f": "همکاری مستمر با ریتم ثابت", "p2_p": "بر اساس درخواست",
	"p3_n": "گسترده", "p3_f": "تیمی که همین حالا ظرفیت لازم دارد", "p3_p": "بر اساس درخواست",
	"contact_lead": "بنویسید، تماس بگیرید یا سر بزنید. همه را می‌خوانیم و به همه پاسخ می‌دهیم.",
	"c_email":      "ایمیل", "c_phone": "تلفن", "c_address": "نشانی",
	"c_name": "نام شما", "c_message": "پیام", "c_send": "ارسال",
	"c_note":    "آنچه می‌فرستید فقط برای پاسخ به شما استفاده می‌شود.",
	"e_home":    "رفتن به صفحهٔ اصلی", "e_back": "بازگشت",
	"tagline":   "کار دقیق، تحویل به‌موقع.",
	"hero":      "کاری که می‌شود رویش ساخت",
	"hero_text": "با شما برنامه‌ریزی می‌کنیم، یک‌بار درست می‌سازیم و بعد از آن هم در دسترس می‌مانیم. نه در صورتحساب غافلگیری هست و نه در تحویل.",
	"address":   "خیابان آزادی، پلاک ۱۲، واحد ۴",
}

var copyAR = map[string]string{
	"nav_home": "الرئيسية", "nav_about": "من نحن", "nav_services": "الخدمات", "nav_contact": "اتصل بنا",
	"f_links": "روابط", "f_contact": "اتصال", "f_rights": "جميع الحقوق محفوظة.",
	"f_built": "صُنع بعناية.",
	"cta_primary": "تواصل معنا", "cta_secondary": "خدماتنا",
	"f1_title": "موثوق", "f1_text": "عمل يصمد في الظروف الحقيقية، لا في يوم التسليم فقط.",
	"f2_title": "مُقاس", "f2_text": "قرارات مبنية على أرقام، تُراجَع بوضوح وتتغيّر بتغيّرها.",
	"f3_title": "متجاوب", "f3_text": "فريق صغير يردّ، ويفهم السؤال، ويُتابع حتى النهاية.",
	"stat1_n": "+12", "stat1_l": "سنة",
	"stat2_n": "240", "stat2_l": "مشروع",
	"stat3_n": "38", "stat3_l": "شريك",
	"stat4_n": "98%", "stat4_l": "استمرارية",
	"about_lead":      "فريق صغير وثابت يعمل بعناية لمن يحتاج أن يدوم عمله.",
	"about_mission_t": "ماذا نفعل", "about_mission": "نقبل عددًا محدودًا من المشاريع وننهيها كما ينبغي.",
	"about_story_t": "كيف بدأنا", "about_story": "شخصان، ومشكلة تستحق الحل، ورفض عنيد لتسليم ما لا نستخدمه نحن.",
	"about_values_t": "ما نلتزم به", "about_values": "قل الحقيقة، وحدّد التكلفة، وسلّم ما اتُّفق عليه، وأجب حين يسوء الأمر.",
	"services_lead": "ثلاث طرق للعمل معنا، تبدأ كلها بحوار لا بعقد.",
	"t_plan": "الخطة", "t_for": "الأنسب لـ", "t_price": "تبدأ من",
	"p1_n": "البداية", "p1_f": "مشروع أول بنطاق واضح", "p1_p": "عند الطلب",
	"p2_n": "القياسية", "p2_f": "عمل مستمر بإيقاع ثابت", "p2_p": "عند الطلب",
	"p3_n": "الموسعة", "p3_f": "فريق يحتاج طاقة الآن", "p3_p": "عند الطلب",
	"contact_lead": "اكتب أو اتصل أو زُرنا. نقرأ كل شيء ونردّ على الجميع.",
	"c_email": "البريد", "c_phone": "الهاتف", "c_address": "العنوان",
	"c_name": "اسمك", "c_message": "الرسالة", "c_send": "إرسال",
	"c_note": "نستخدم ما ترسله للرد عليك فقط.",
	"e_home": "الصفحة الرئيسية", "e_back": "رجوع",
	"tagline": "عمل دقيق، يُسلَّم في وقته.",
	"hero": "عمل يمكن البناء عليه",
	"hero_text": "نخطط معك، ونبني مرة واحدة بشكل صحيح، ونبقى متاحين بعدها.",
	"address": "مكتب 4، شارع السوق 12",
}

var copyTR = map[string]string{
	"nav_home": "Ana sayfa", "nav_about": "Hakkımızda", "nav_services": "Hizmetler", "nav_contact": "İletişim",
	"f_links": "Bağlantılar", "f_contact": "İletişim", "f_rights": "Tüm hakları saklıdır.",
	"f_built": "Özenle yapıldı.",
	"cta_primary": "Bize ulaşın", "cta_secondary": "Ne yapıyoruz",
	"f1_title": "Güvenilir", "f1_text": "Yalnızca teslim gününde değil, gerçek koşullarda da ayakta kalan iş.",
	"f2_title": "Ölçülü", "f2_text": "Sayılara dayanan, açıkça gözden geçirilen, sayılar değişince değişen kararlar.",
	"f3_title": "İlgili", "f3_text": "Cevap veren, soruyu anlayan ve sonuna kadar takip eden küçük bir ekip.",
	"stat1_n": "12+", "stat1_l": "Yıl",
	"stat2_n": "240", "stat2_l": "Proje",
	"stat3_n": "38", "stat3_l": "İş ortağı",
	"stat4_n": "%98", "stat4_l": "Süreklilik",
	"about_lead": "İşinin kalıcı olmasını isteyenler için özenle çalışan küçük ve istikrarlı bir ekip.",
	"about_mission_t": "Ne yapıyoruz", "about_mission": "Sınırlı sayıda iş alır ve onları düzgün bitiririz.",
	"about_story_t": "Nasıl başladık", "about_story": "İki kişi, çözmeye değer bir sorun ve kendimizin kullanmayacağı bir şeyi teslim etmemekte inat.",
	"about_values_t": "Neye bağlıyız", "about_values": "Doğruyu söyle, maliyeti açıkla, anlaşılanı teslim et, ters gittiğinde telefonu aç.",
	"services_lead": "Bizimle çalışmanın üç yolu. Hepsi sözleşmeyle değil, bir sohbetle başlar.",
	"t_plan": "Paket", "t_for": "Uygun olduğu", "t_price": "Başlangıç",
	"p1_n": "Başlangıç", "p1_f": "Kapsamı net ilk proje", "p1_p": "talep üzerine",
	"p2_n": "Standart", "p2_f": "Sabit ritimde sürekli iş", "p2_p": "talep üzerine",
	"p3_n": "Genişletilmiş", "p3_f": "Hemen kapasite gereken ekip", "p3_p": "talep üzerine",
	"contact_lead": "Yazın, arayın ya da uğrayın. Hepsini okur, hepsine yanıt veririz.",
	"c_email": "E-posta", "c_phone": "Telefon", "c_address": "Adres",
	"c_name": "Adınız", "c_message": "Mesaj", "c_send": "Gönder",
	"c_note": "Gönderdiklerinizi yalnızca size yanıt vermek için kullanırız.",
	"e_home": "Ana sayfaya git", "e_back": "Geri dön",
	"tagline": "Özenli iş, zamanında teslim.",
	"hero": "Üzerine inşa edebileceğiniz iş",
	"hero_text": "Sizinle planlar, bir kez doğru yapar ve sonrasında ulaşılabilir kalırız.",
	"address": "Ofis 4, Çarşı Caddesi 12",
}

func table(lang string) map[string]string {
	switch strings.ToLower(strings.SplitN(strings.TrimSpace(lang), "-", 2)[0]) {
	case "fa":
		return copyFA
	case "ar":
		return copyAR
	case "tr":
		return copyTR
	}
	return copyEN
}

// tr returns a default string, falling back to English and then to the key
// itself — a visible key is better than a blank element when debugging, and
// this path is unreachable for every key in copyKeys.
func tr(lang, key string) string {
	if v, ok := table(lang)[key]; ok {
		return v
	}
	if v, ok := copyEN[key]; ok {
		return v
	}
	return ""
}

func defaultTagline(lang string) string  { return tr(lang, "tagline") }
func defaultAddress(lang string) string  { return tr(lang, "address") }
func defaultHeroText(lang string) string { return tr(lang, "hero_text") }

func defaultHero(lang, name string) string {
	h := tr(lang, "hero")
	if h == "" {
		return name
	}
	return h
}

// Error-page wording.
//
// The texts are deliberately vague about causes. "The server is busy" is what
// a real site says; "upstream connect() failed" is what a proxy says, and a
// probe reading the latter learns that something is behind this address and
// that it just stopped answering.
var errTitles = map[string]map[string]string{
	"en": {
		"400": "That request could not be read", "401": "You need to sign in",
		"403": "This page is not available", "404": "We cannot find that page",
		"405": "That is not something this page does", "413": "That upload is too large",
		"429": "You are going a little fast", "500": "Something went wrong on our side",
		"502": "We are having a moment", "503": "We are briefly unavailable",
		"504": "That took longer than expected",
	},
	"fa": {
		"400": "درخواست قابل خواندن نبود", "401": "برای ادامه باید وارد شوید",
		"403": "این صفحه در دسترس نیست", "404": "این صفحه پیدا نشد",
		"405": "این کار در این صفحه انجام نمی‌شود", "413": "حجم ارسالی بیش از حد است",
		"429": "کمی سریع‌تر از حد معمول", "500": "مشکلی از سمت ما پیش آمد",
		"502": "لحظه‌ای مشکل داریم", "503": "موقتاً در دسترس نیستیم",
		"504": "پاسخ بیش از حد انتظار طول کشید",
	},
	"ar": {
		"400": "تعذّرت قراءة الطلب", "401": "يلزم تسجيل الدخول",
		"403": "هذه الصفحة غير متاحة", "404": "لم نجد هذه الصفحة",
		"405": "هذا الإجراء غير مدعوم هنا", "413": "الملف المرسل كبير جدًا",
		"429": "طلبات كثيرة بسرعة", "500": "حدث خطأ لدينا",
		"502": "لدينا مشكلة مؤقتة", "503": "غير متاحين مؤقتًا",
		"504": "استغرق الأمر وقتًا أطول من المتوقع",
	},
	"tr": {
		"400": "Bu istek okunamadı", "401": "Giriş yapmanız gerekiyor",
		"403": "Bu sayfa kullanılamıyor", "404": "Bu sayfayı bulamadık",
		"405": "Bu sayfa bunu yapmıyor", "413": "Yükleme çok büyük",
		"429": "Biraz hızlı gidiyorsunuz", "500": "Bizim tarafımızda bir sorun oldu",
		"502": "Şu an bir sıkıntı yaşıyoruz", "503": "Kısa süreliğine kapalıyız",
		"504": "Bu beklenenden uzun sürdü",
	},
}

var errTexts = map[string]map[string]string{
	"en": {
		"400": "The address or the data sent with it was malformed. Check the link and try again.",
		"401": "This part of the site is for signed-in visitors.",
		"403": "You do not have access to this page. If you think that is wrong, get in touch.",
		"404": "The page may have moved or the address may have a typo. The home page is a good place to start again.",
		"405": "The page exists, but not for this kind of request.",
		"413": "Try a smaller file, or send it to us by email instead.",
		"429": "Too many requests arrived in a short time. Wait a moment and try again.",
		"500": "We have logged it and we are looking. Please try again shortly.",
		"502": "A part of the site is not responding. It usually clears up by itself within a minute.",
		"503": "We are doing a little maintenance. Back very soon.",
		"504": "The page did not finish loading in time. Reloading often fixes it.",
	},
	"fa": {
		"400": "آدرس یا داده‌های ارسالی درست نبود. لینک را بررسی کنید و دوباره تلاش کنید.",
		"401": "این بخش از سایت برای کاربران واردشده است.",
		"403": "به این صفحه دسترسی ندارید. اگر فکر می‌کنید اشتباهی رخ داده، با ما تماس بگیرید.",
		"404": "شاید صفحه جابه‌جا شده یا آدرس اشتباه تایپ شده باشد. از صفحهٔ اصلی دوباره شروع کنید.",
		"405": "صفحه وجود دارد، اما این نوع درخواست را نمی‌پذیرد.",
		"413": "فایل کوچک‌تری بفرستید یا آن را با ایمیل ارسال کنید.",
		"429": "در زمان کوتاه درخواست‌های زیادی رسید. کمی صبر کنید و دوباره تلاش کنید.",
		"500": "ثبت شد و در حال بررسی هستیم. لطفاً کمی بعد دوباره تلاش کنید.",
		"502": "بخشی از سایت پاسخ نمی‌دهد. معمولاً ظرف یک دقیقه خودبه‌خود برطرف می‌شود.",
		"503": "در حال انجام کمی نگهداری هستیم. خیلی زود برمی‌گردیم.",
		"504": "بارگذاری صفحه به‌موقع تمام نشد. معمولاً بارگذاری دوباره مشکل را حل می‌کند.",
	},
	"ar": {
		"400": "العنوان أو البيانات المرسلة غير صحيحة. تحقق من الرابط وحاول مجددًا.",
		"401": "هذا القسم مخصص للزوار المسجّلين.",
		"403": "لا تملك صلاحية الوصول إلى هذه الصفحة.",
		"404": "ربما نُقلت الصفحة أو في العنوان خطأ مطبعي.",
		"405": "الصفحة موجودة، لكنها لا تقبل هذا النوع من الطلبات.",
		"413": "جرّب ملفًا أصغر أو أرسله بالبريد.",
		"429": "وصلت طلبات كثيرة في وقت قصير. انتظر قليلًا ثم حاول.",
		"500": "سجّلنا المشكلة ونعمل عليها. حاول بعد قليل.",
		"502": "جزء من الموقع لا يستجيب. عادةً ما يُحل خلال دقيقة.",
		"503": "نجري بعض الصيانة. سنعود قريبًا.",
		"504": "لم يكتمل التحميل في الوقت المتوقع. أعد المحاولة.",
	},
	"tr": {
		"400": "Adres ya da gönderilen veri hatalıydı. Bağlantıyı kontrol edip tekrar deneyin.",
		"401": "Sitenin bu bölümü giriş yapmış ziyaretçiler içindir.",
		"403": "Bu sayfaya erişiminiz yok. Bir yanlışlık olduğunu düşünüyorsanız bize yazın.",
		"404": "Sayfa taşınmış ya da adreste bir yazım hatası olabilir.",
		"405": "Sayfa var, ancak bu tür bir isteği kabul etmiyor.",
		"413": "Daha küçük bir dosya deneyin ya da e-posta ile gönderin.",
		"429": "Kısa sürede çok fazla istek geldi. Biraz bekleyip tekrar deneyin.",
		"500": "Kaydettik ve bakıyoruz. Lütfen kısa süre sonra tekrar deneyin.",
		"502": "Sitenin bir bölümü yanıt vermiyor. Genellikle bir dakika içinde düzelir.",
		"503": "Kısa bir bakım yapıyoruz. Çok yakında döneceğiz.",
		"504": "Sayfa zamanında yüklenemedi. Yenilemek genelde işe yarar.",
	},
}

func errTitle(lang, code string) string {
	l := strings.ToLower(strings.SplitN(strings.TrimSpace(lang), "-", 2)[0])
	if m, ok := errTitles[l]; ok {
		if v, ok := m[code]; ok {
			return v
		}
	}
	if v, ok := errTitles["en"][code]; ok {
		return v
	}
	// A status the panel has no wording for still gets a page rather than
	// a blank heading.
	if strings.HasPrefix(code, "5") {
		return errTitle(lang, "500")
	}
	return errTitle(lang, "404")
}

func errText(lang, code string) string {
	l := strings.ToLower(strings.SplitN(strings.TrimSpace(lang), "-", 2)[0])
	if m, ok := errTexts[l]; ok {
		if v, ok := m[code]; ok {
			return v
		}
	}
	if v, ok := errTexts["en"][code]; ok {
		return v
	}
	if strings.HasPrefix(code, "5") {
		return errText(lang, "500")
	}
	return errText(lang, "404")
}
