package main

import (
	"context"
	"errors"
	"os/user"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// Locale is one system locale: Code is what `locale -a` prints and what
// LANG is set to; Name is how the Language page shows it.
type Locale struct{ Code, Name string }

// Locales lists the UTF-8 locales of the system (`locale -a`, from
// musl-locales), named in their own language and sorted by name. C and
// POSIX, and every locale that is not UTF-8, are left out. Where a locale
// exists as both de_DE.UTF-8 and de_DE.utf8, the first spelling is kept.
func Locales(r Runner) ([]Locale, error) {
	out, err := r.Run(context.Background(), "locale", "-a")
	if err != nil {
		return nil, err
	}
	return parseLocales(string(out)), nil
}

func parseLocales(out string) []Locale {
	var list []Locale
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		code := strings.TrimSpace(line)
		base, enc, ok := strings.Cut(code, ".")
		if !ok || base == "" {
			continue
		}
		enc, mod, _ := strings.Cut(enc, "@")
		if e := strings.ToLower(enc); e != "utf-8" && e != "utf8" {
			continue
		}
		key := base + "@" + mod
		if seen[key] {
			continue
		}
		seen[key] = true
		name := localeName(base)
		if mod != "" {
			name += " @" + mod
		}
		list = append(list, Locale{Code: code, Name: name})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Code < list[j].Code
	})
	return list
}

// covered says every rune of s is in the interface font's coverage (Inter:
// Latin, Greek and Cyrillic up to U+052F, Latin Extended Additional, general
// punctuation). guikit loads no system fonts, so a name in another script
// would draw as boxes; the page shows the English name instead.
func covered(s string) bool {
	for _, r := range s {
		switch {
		case r <= 0x052F, r >= 0x1E00 && r <= 0x1EFF, r >= 0x2000 && r <= 0x206F:
		default:
			return false
		}
	}
	return true
}

// englishName is the English name of a locale code whose own name is shown
// in another form, "" when there is none. The search matches it, so
// "chinese" finds zh_CN even where the page shows the native name.
func englishName(code string) string {
	base, _, _ := strings.Cut(code, ".")
	if n, ok := englishNames[base]; ok {
		return n
	}
	return ""
}

// localeName is the name of a locale without its encoding ("de_DE"): the
// table's, else the language's with the territory code, else the code.
func localeName(base string) string {
	if n, ok := localeNames[base]; ok {
		if !covered(n) {
			return englishNames[base]
		}
		return n
	}
	lang, terr, hasTerr := strings.Cut(base, "_")
	if n, ok := languageNames[lang]; ok {
		if !covered(n) {
			n = englishLanguages[lang]
		}
		if hasTerr {
			return n + " (" + terr + ")"
		}
		return n
	}
	if base == "C" {
		return i18n.T("Default (C.UTF-8)")
	}
	return base
}

// CurrentLocale is the LANG of an /etc/locale.conf, or "" when it has none.
func CurrentLocale(localeConf string) string {
	lang := ""
	for _, line := range strings.Split(localeConf, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "LANG" {
			continue
		}
		lang = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return lang
}

// LanguageSet says the LANG of /etc/locale.conf is a language somebody
// picked. The image ships no /etc/locale.conf, so a machine whose user never
// chose has no LANG; C, POSIX and C.UTF-8 are the neutral values and count
// as not chosen, so that user still sees the Language page.
func LanguageSet(lang string) bool {
	switch strings.ToUpper(strings.TrimSpace(lang)) {
	case "", "C", "POSIX", "C.UTF-8", "C.UTF8":
		return false
	}
	return true
}

// LocaleName is the display name of a locale code ("de_DE.UTF-8"), for the
// current locale, which `locale -a` may spell differently.
func LocaleName(code string) string {
	base, enc, _ := strings.Cut(code, ".")
	_, mod, _ := strings.Cut(enc, "@")
	name := localeName(base)
	if mod != "" {
		name += " @" + mod
	}
	return name
}

// Settings changes the machine's settings through isotope-updated.
type Settings interface {
	// SetLocale sets the system language. ErrNotAuthorized means the
	// daemon refused: the user is not an administrator.
	SetLocale(lang string) error
}

// ErrNotAuthorized is the daemon refusing a call (polkit said no, or the
// caller runs in a Flatpak sandbox).
var ErrNotAuthorized = errors.New("not authorized")

// daemonSettings is Settings over the system bus.
type daemonSettings struct{}

const (
	updateBus       = "io.kairos.IsotopeUpdate1"
	updatePath      = dbus.ObjectPath("/io/kairos/IsotopeUpdate1")
	updateNotAuthed = updateBus + ".Error.NotAuthorized"
)

func (daemonSettings) SetLocale(lang string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	return mapSettingsErr(conn.Object(updateBus, updatePath).Call(updateBus+".SetLocale", 0, lang).Err)
}

// mapSettingsErr turns the daemon's NotAuthorized into ErrNotAuthorized
// and keeps the daemon's words for any other error.
func mapSettingsErr(err error) error {
	var de dbus.Error
	if errors.As(err, &de) {
		if de.Name == updateNotAuthed {
			return ErrNotAuthorized
		}
		if len(de.Body) > 0 {
			if s, ok := de.Body[0].(string); ok {
				return errors.New(s)
			}
		}
	}
	return err
}

// localeNames are the names of the common locales, each in its own
// language: the language, then the territory in that language. Checked by
// hand; a locale not here gets its language's name and the territory code
// (languageNames), or its code.
var localeNames = map[string]string{
	"af_ZA": "Afrikaans (Suid-Afrika)",
	"ar_AE": "العربية (الإمارات العربية المتحدة)",
	"ar_EG": "العربية (مصر)",
	"ar_SA": "العربية (المملكة العربية السعودية)",
	"be_BY": "беларуская (Беларусь)",
	"bg_BG": "български (България)",
	"bn_BD": "বাংলা (বাংলাদেশ)",
	"bn_IN": "বাংলা (ভারত)",
	"bs_BA": "bosanski (Bosna i Hercegovina)",
	"ca_ES": "català (Espanya)",
	"cs_CZ": "čeština (Česko)",
	"cy_GB": "Cymraeg (Y Deyrnas Unedig)",
	"da_DK": "dansk (Danmark)",
	"de_AT": "Deutsch (Österreich)",
	"de_BE": "Deutsch (Belgien)",
	"de_CH": "Deutsch (Schweiz)",
	"de_DE": "Deutsch (Deutschland)",
	"de_LU": "Deutsch (Luxemburg)",
	"el_GR": "Ελληνικά (Ελλάδα)",
	"en_AU": "English (Australia)",
	"en_CA": "English (Canada)",
	"en_GB": "English (United Kingdom)",
	"en_IE": "English (Ireland)",
	"en_IN": "English (India)",
	"en_NZ": "English (New Zealand)",
	"en_US": "English (United States)",
	"en_ZA": "English (South Africa)",
	"es_AR": "español (Argentina)",
	"es_CL": "español (Chile)",
	"es_CO": "español (Colombia)",
	"es_ES": "español (España)",
	"es_MX": "español (México)",
	"es_PE": "español (Perú)",
	"es_US": "español (Estados Unidos)",
	"es_VE": "español (Venezuela)",
	"et_EE": "eesti (Eesti)",
	"eu_ES": "euskara (Espainia)",
	"fa_IR": "فارسی (ایران)",
	"fi_FI": "suomi (Suomi)",
	"fr_BE": "français (Belgique)",
	"fr_CA": "français (Canada)",
	"fr_CH": "français (Suisse)",
	"fr_FR": "français (France)",
	"fr_LU": "français (Luxembourg)",
	"ga_IE": "Gaeilge (Éire)",
	"gl_ES": "galego (España)",
	"he_IL": "עברית (ישראל)",
	"hi_IN": "हिन्दी (भारत)",
	"hr_HR": "hrvatski (Hrvatska)",
	"hu_HU": "magyar (Magyarország)",
	"hy_AM": "հայերեն (Հայաստան)",
	"id_ID": "Indonesia (Indonesia)",
	"is_IS": "íslenska (Ísland)",
	"it_CH": "italiano (Svizzera)",
	"it_IT": "italiano (Italia)",
	"ja_JP": "日本語 (日本)",
	"ka_GE": "ქართული (საქართველო)",
	"kk_KZ": "қазақ тілі (Қазақстан)",
	"ko_KR": "한국어 (대한민국)",
	"lt_LT": "lietuvių (Lietuva)",
	"lv_LV": "latviešu (Latvija)",
	"mk_MK": "македонски (Македонија)",
	"ms_MY": "Melayu (Malaysia)",
	"nb_NO": "norsk bokmål (Norge)",
	"nl_BE": "Nederlands (België)",
	"nl_NL": "Nederlands (Nederland)",
	"nn_NO": "norsk nynorsk (Noreg)",
	"pl_PL": "polski (Polska)",
	"pt_BR": "português (Brasil)",
	"pt_PT": "português (Portugal)",
	"ro_RO": "română (România)",
	"ru_RU": "русский (Россия)",
	"ru_UA": "русский (Украина)",
	"sk_SK": "slovenčina (Slovensko)",
	"sl_SI": "slovenščina (Slovenija)",
	"sq_AL": "shqip (Shqipëri)",
	"sr_RS": "српски (Србија)",
	"sv_FI": "svenska (Finland)",
	"sv_SE": "svenska (Sverige)",
	"ta_IN": "தமிழ் (இந்தியா)",
	"th_TH": "ไทย (ไทย)",
	"tr_TR": "Türkçe (Türkiye)",
	"uk_UA": "українська (Україна)",
	"ur_PK": "اردو (پاکستان)",
	"uz_UZ": "oʻzbekcha (Oʻzbekiston)",
	"vi_VN": "Tiếng Việt (Việt Nam)",
	"zh_CN": "中文 (中国)",
	"zh_HK": "中文 (中国香港特别行政区)",
	"zh_SG": "中文 (新加坡)",
	"zh_TW": "中文 (台灣)",
}

// englishNames are the English names of the localeNames entries that are not
// covered by the interface font (see covered). A test keeps the two in step.
var englishNames = map[string]string{
	"ar_AE": "Arabic (United Arab Emirates)", "ar_EG": "Arabic (Egypt)",
	"ar_SA": "Arabic (Saudi Arabia)", "bn_BD": "Bangla (Bangladesh)",
	"bn_IN": "Bangla (India)", "fa_IR": "Persian (Iran)", "he_IL": "Hebrew (Israel)",
	"hi_IN": "Hindi (India)", "hy_AM": "Armenian (Armenia)", "ja_JP": "Japanese (Japan)",
	"ka_GE": "Georgian (Georgia)", "ko_KR": "Korean (South Korea)", "ta_IN": "Tamil (India)",
	"th_TH": "Thai (Thailand)", "ur_PK": "Urdu (Pakistan)", "zh_CN": "Chinese (China)",
	"zh_HK": "Chinese (Hong Kong)", "zh_SG": "Chinese (Singapore)", "zh_TW": "Chinese (Taiwan)",
}

// englishLanguages are the English names of the languageNames entries that
// are not covered by the interface font.
var englishLanguages = map[string]string{
	"ar": "Arabic", "bn": "Bangla", "fa": "Persian", "he": "Hebrew", "hi": "Hindi",
	"hy": "Armenian", "ja": "Japanese", "ka": "Georgian", "ko": "Korean", "ta": "Tamil",
	"th": "Thai", "ur": "Urdu", "zh": "Chinese",
}

// languageNames are the languages' own names, for a territory the table
// above does not have: "de_XX" shows as "Deutsch (XX)".
var languageNames = map[string]string{
	"af": "Afrikaans", "ar": "العربية", "be": "беларуская", "bg": "български",
	"bn": "বাংলা", "bs": "bosanski", "ca": "català", "cs": "čeština",
	"cy": "Cymraeg", "da": "dansk", "de": "Deutsch", "el": "Ελληνικά",
	"en": "English", "es": "español", "et": "eesti", "eu": "euskara",
	"fa": "فارسی", "fi": "suomi", "fr": "français", "ga": "Gaeilge",
	"gl": "galego", "he": "עברית", "hi": "हिन्दी", "hr": "hrvatski",
	"hu": "magyar", "hy": "հայերեն", "id": "Indonesia", "is": "íslenska",
	"it": "italiano", "ja": "日本語", "ka": "ქართული", "kk": "қазақ тілі",
	"ko": "한국어", "lt": "lietuvių", "lv": "latviešu", "mk": "македонски",
	"ms": "Melayu", "nb": "norsk bokmål", "nl": "Nederlands", "nn": "norsk nynorsk",
	"pl": "polski", "pt": "português", "ro": "română", "ru": "русский",
	"sk": "slovenčina", "sl": "slovenščina", "sq": "shqip", "sr": "српски",
	"sv": "svenska", "ta": "தமிழ்", "th": "ไทย", "tr": "Türkçe",
	"uk": "українська", "ur": "اردو", "uz": "oʻzbekcha", "vi": "Tiếng Việt",
	"zh": "中文",
}

// InAdminGroup says whether the current user is a member of the admin
// group, which polkit's rule for isotope-updated asks too (see
// 50-isotope-update.rules). The page uses it to start read-only; the
// daemon still decides on every call.
func InAdminGroup() bool {
	u, err := user.Current()
	if err != nil {
		return false
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false
	}
	g, err := user.LookupGroup("admin")
	if err != nil {
		return false
	}
	return hasGroup(ids, g.Gid)
}

func hasGroup(ids []string, gid string) bool {
	for _, id := range ids {
		if id == gid {
			return true
		}
	}
	return false
}
