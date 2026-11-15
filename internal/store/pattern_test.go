package store

import "testing"

func TestContainsPattern(t *testing.T) {
	tests := []struct{ in, want string }{
		{"matrix", `%matrix%`},
		{"100%", `%100\%%`},
		{"snake_case", `%snake\_case%`},
		{`back\slash`, `%back\\slash%`},
		{`\%_`, `%\\\%\_%`},
		{"", `%%`},
	}
	for _, tt := range tests {
		if got := containsPattern(tt.in); got != tt.want {
			t.Errorf("containsPattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
