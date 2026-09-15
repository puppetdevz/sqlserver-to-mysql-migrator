package matcher

import "testing"

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"name", "`name`"},
		{"a`b", "`a``b`"},
		{"", "``"},
		{"user_id", "`user_id`"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := QuoteIdent(tt.input); got != tt.want {
				t.Fatalf("QuoteIdent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
