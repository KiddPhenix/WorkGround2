package work

import (
	"fmt"
	"strings"
)

const (
	LocaleEnglish     = "en"
	LocaleChinese     = "zh"
	LocaleTraditional = "zh-TW"
)

// NormalizeLocale returns the canonical Work locale. Empty values remain
// compatible with older callers by selecting English; unsupported explicit
// values fail visibly instead of silently changing the output language.
func NormalizeLocale(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "en", "en-us", "en-gb":
		return LocaleEnglish, nil
	case "zh", "zh-cn", "zh-hans":
		return LocaleChinese, nil
	case "zh-tw", "zh-hant", "zh-hk", "zh-mo":
		return LocaleTraditional, nil
	default:
		return "", fmt.Errorf("work: unsupported locale %q", strings.TrimSpace(value))
	}
}

// LocaleDirective returns the shared model instruction for user-visible Work
// content. Empty/unknown persisted values are left unconstrained for legacy
// Works; every newly created Work stores a normalized locale.
func LocaleDirective(locale string) string {
	switch strings.TrimSpace(locale) {
	case LocaleEnglish:
		return "Use English for all user-visible business text, including Work and node titles, descriptions, Block titles and content, input questions and choice labels, artifact titles, and generated file base names. Keep IDs, enum values, kinds, tool hints, schema fields, and required file extensions unchanged."
	case LocaleChinese:
		return "Use Simplified Chinese for all user-visible business text, including Work and node titles, descriptions, Block titles and content, input questions and choice labels, artifact titles, and generated file base names. Keep IDs, enum values, kinds, tool hints, schema fields, and required file extensions unchanged."
	case LocaleTraditional:
		return "Use Traditional Chinese for all user-visible business text, including Work and node titles, descriptions, Block titles and content, input questions and choice labels, artifact titles, and generated file base names. Keep IDs, enum values, kinds, tool hints, schema fields, and required file extensions unchanged."
	default:
		return ""
	}
}

func defaultWorkName(locale string) string {
	switch locale {
	case LocaleChinese, LocaleTraditional:
		return "新工作"
	default:
		return "New Work"
	}
}
