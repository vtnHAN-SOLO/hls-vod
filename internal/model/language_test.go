package model

import "testing"

func TestSubtitleLanguage(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"eng", "en"}, {"en", "en"}, {"ger", "de"}, {"deu", "de"}, {"fre", "fr"}, {"chi", "zh"},
		{"vie", "vi"}, {"fil", "fil"}, {"und", "und"}, {"", "und"}, {"e", "und"},
		{"engl", "und"}, {"EN", "und"}, {"e1g", "und"},
	} {
		if got := SubtitleLanguage(tt.in); got != tt.want {
			t.Errorf("SubtitleLanguage(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
