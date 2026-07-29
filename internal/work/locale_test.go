package work

import (
	"strings"
	"testing"
)

func TestNormalizeLocale(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{"", "en", false},
		{"en", "en", false},
		{"EN", "en", false},
		{"en-US", "en", false},
		{"en-gb", "en", false},
		{"zh", "zh", false},
		{"ZH", "zh", false},
		{"zh-CN", "zh", false},
		{"zh-hans", "zh", false},
		{"zh-TW", "zh-TW", false},
		{"zh-tw", "zh-TW", false},
		{"zh-Hant", "zh-TW", false},
		{"zh-HK", "zh-TW", false},
		{"zh-mo", "zh-TW", false},
		{"fr", "", true},
		{"de", "", true},
		{"  zh  ", "zh", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := NormalizeLocale(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeLocale(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("NormalizeLocale(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestLocaleDirective(t *testing.T) {
	tests := []struct {
		locale       string
		wantContains string
		wantMissing  []string
	}{
		{
			locale:       "en",
			wantContains: "English",
			wantMissing:  nil,
		},
		{
			locale:       "zh",
			wantContains: "Simplified Chinese",
			wantMissing:  nil,
		},
		{
			locale:       "zh-TW",
			wantContains: "Traditional Chinese",
			wantMissing:  nil,
		},
		{
			locale:       "",
			wantContains: "",
			wantMissing:  nil,
		},
		{
			locale:       "fr",
			wantContains: "",
			wantMissing:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			got := LocaleDirective(tt.locale)
			if tt.wantContains == "" && got != "" {
				t.Errorf("LocaleDirective(%q) = %q, want empty", tt.locale, got)
			}
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("LocaleDirective(%q) missing %q in: %s", tt.locale, tt.wantContains, got)
			}
			// All directives must mention not translating technical fields
			if got != "" {
				mustHave := []string{"IDs", "enum", "kinds", "tool hints"}
				for _, phrase := range mustHave {
					if !strings.Contains(strings.ToLower(got), strings.ToLower(phrase)) {
						t.Errorf("LocaleDirective(%q) missing technical-field instruction %q", tt.locale, phrase)
					}
				}
			}
		})
	}
}

func TestDefaultWorkName(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{"en", "New Work"},
		{"zh", "新工作"},
		{"zh-TW", "新工作"},
		{"", "New Work"},
	}
	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			got := defaultWorkName(tt.locale)
			if got != tt.want {
				t.Errorf("defaultWorkName(%q) = %q, want %q", tt.locale, got, tt.want)
			}
		})
	}
}
