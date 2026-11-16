package model

// SubtitleLanguage turns a stored subtitle language tag (ISO 639-2 as the
// source had it, bibliographic or terminologic; or a 2-letter ISO 639-1
// code) into the BCP 47 tag browsers expect in <track srclang> and
// Intl.DisplayNames: the 2-letter code where one exists ("eng" and "en" →
// "en", "ger" and "deu" → "de"), else the 3-letter code itself, which is
// valid BCP 47 ("fil", "haw"). Anything unknown or malformed is "und".
func SubtitleLanguage(tag string) string {
	if v, ok := iso6392to1[tag]; ok {
		return v
	}
	if len(tag) < 2 || len(tag) > 3 {
		return "und"
	}
	for _, r := range tag {
		if r < 'a' || r > 'z' {
			return "und"
		}
	}
	return tag
}

// iso6392to1 maps the ISO 639-2 codes (B and T forms) of languages that have
// an ISO 639-1 code. Not exhaustive: a missing one keeps its 3-letter code,
// which BCP 47 still accepts.
var iso6392to1 = map[string]string{
	"afr": "af", "alb": "sq", "sqi": "sq", "amh": "am", "ara": "ar", "arm": "hy", "hye": "hy",
	"aze": "az", "baq": "eu", "eus": "eu", "bel": "be", "ben": "bn", "bos": "bs", "bul": "bg",
	"bur": "my", "mya": "my", "cat": "ca", "chi": "zh", "zho": "zh", "hrv": "hr", "cze": "cs",
	"ces": "cs", "dan": "da", "dut": "nl", "nld": "nl", "eng": "en", "est": "et", "fin": "fi",
	"fre": "fr", "fra": "fr", "geo": "ka", "kat": "ka", "ger": "de", "deu": "de", "gle": "ga",
	"glg": "gl", "gre": "el", "ell": "el", "guj": "gu", "heb": "he", "hin": "hi", "hun": "hu",
	"ice": "is", "isl": "is", "ind": "id", "ita": "it", "jpn": "ja", "kan": "kn", "kaz": "kk",
	"khm": "km", "kor": "ko", "kur": "ku", "lao": "lo", "lat": "la", "lav": "lv", "lit": "lt",
	"mac": "mk", "mkd": "mk", "may": "ms", "msa": "ms", "mal": "ml", "mlt": "mt", "mar": "mr",
	"mon": "mn", "nep": "ne", "nor": "no", "nob": "nb", "nno": "nn", "per": "fa", "fas": "fa",
	"pol": "pl", "por": "pt", "pan": "pa", "rum": "ro", "ron": "ro", "rus": "ru", "srp": "sr",
	"sin": "si", "slo": "sk", "slk": "sk", "slv": "sl", "som": "so", "spa": "es", "swa": "sw",
	"swe": "sv", "tgl": "tl", "tam": "ta", "tel": "te", "tha": "th", "tur": "tr", "ukr": "uk",
	"urd": "ur", "uzb": "uz", "vie": "vi", "wel": "cy", "cym": "cy", "yid": "yi", "zul": "zu",
	"bre": "br", "cos": "co", "fao": "fo", "fry": "fy", "gla": "gd", "hat": "ht", "ltz": "lb",
	"mao": "mi", "mri": "mi", "oci": "oc", "que": "qu", "roh": "rm", "san": "sa", "tat": "tt",
	"tib": "bo", "bod": "bo", "tuk": "tk", "uig": "ug", "xho": "xh", "yor": "yo", "ibo": "ig",
	"hau": "ha", "kin": "rw", "mlg": "mg", "nya": "ny", "orm": "om", "sna": "sn", "tir": "ti",
	"tsn": "tn", "wol": "wo", "epo": "eo", "ina": "ia", "jav": "jv", "sun": "su", "tgk": "tg",
	"kir": "ky", "pus": "ps", "snd": "sd", "asm": "as", "ori": "or", "div": "dv",
}
