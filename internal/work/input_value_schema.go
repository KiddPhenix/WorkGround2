package work

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// ── Value schema constraints ────────────────────────────────────────────────

// valueSchemaHeader is the common wrapper for ValueSchema JSON.
type valueSchemaHeader struct {
	Kind string `json:"kind"`
}

// TextConstraints defines optional length and pattern rules for text input.
type TextConstraints struct {
	MinLength int    `json:"minLength,omitempty"`
	MaxLength int    `json:"maxLength,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
}

// NumberConstraints defines optional range and unit rules for number input.
type NumberConstraints struct {
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Integer  bool     `json:"integer,omitempty"`
	Unit     string   `json:"unit,omitempty"` // number | amount | ratio | percent
	Currency string   `json:"currency,omitempty"`
}

// DateConstraints defines optional date range rules.
type DateConstraints struct {
	MinDate string `json:"minDate,omitempty"` // ISO 8601 date
	MaxDate string `json:"maxDate,omitempty"`
	Mode    string `json:"mode,omitempty"` // date | time | datetime | range
}

// ChoiceOption is a single selectable option.
type ChoiceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ChoiceConstraints defines option set for single-choice input.
type ChoiceConstraints struct {
	Options    []ChoiceOption `json:"options"`
	AllowOther bool           `json:"allowOther,omitempty"`
}

// MultiChoiceConstraints defines option set for multi-choice input.
type MultiChoiceConstraints struct {
	Options    []ChoiceOption `json:"options"`
	MinSelect  int            `json:"minSelect,omitempty"`
	MaxSelect  int            `json:"maxSelect,omitempty"`
	AllowOther bool           `json:"allowOther,omitempty"`
}

// RosterConstraints defines entry rules for roster input.
type RosterConstraints struct {
	MinEntries int      `json:"minEntries,omitempty"`
	MaxEntries int      `json:"maxEntries,omitempty"`
	Fields     []string `json:"fields,omitempty"`
}

// FormFieldSpec is a field definition inside a form value schema.
type FormFieldSpec struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Kind        InputKind       `json:"kind"`
	Required    bool            `json:"required"`
	ValueSchema json.RawMessage `json:"valueSchema,omitempty"`
}

// FormConstraints defines nested fields for form input.
type FormConstraints struct {
	Fields []FormFieldSpec `json:"fields"`
}

// FileConstraints defines file acceptance rules.
type FileConstraints struct {
	AcceptTypes []string `json:"acceptTypes,omitempty"`
	MaxCount    int      `json:"maxCount,omitempty"`
}

// ApprovalConstraints defines approval risk and description.
type ApprovalConstraints struct {
	RiskLevel   string `json:"riskLevel,omitempty"`
	Description string `json:"description,omitempty"`
}

// ── Known kind set ─────────────────────────────────────────────────────────

var knownInputKinds = map[InputKind]bool{
	InputText:        true,
	InputNumber:      true,
	InputDate:        true,
	InputChoice:      true,
	InputMultiChoice: true,
	InputFile:        true,
	InputRoster:      true,
	InputForm:        true,
	InputApproval:    true,
}

// ── Validation entry point ──────────────────────────────────────────────────

// ValidateInputValue checks that v conforms to spec.Kind and spec.ValueSchema.
// A nil/empty ValueSchema applies basic kind-level validation (e.g. text→string,
// number→number). spec.Required enforces non-null/non-empty.
func ValidateInputValue(spec InputSpec, v json.RawMessage) error {
	if !knownInputKinds[spec.Kind] {
		return fmt.Errorf("work: unknown InputKind %q", spec.Kind)
	}

	// Required check: nil, "null", empty string, empty array, empty object.
	if spec.Required && isEmptyJSON(v) {
		return fmt.Errorf("work: input %q (%s) is required but value is empty", spec.ID, spec.Kind)
	}

	if v == nil || string(v) == "null" {
		if !spec.Required {
			return nil
		}
		return fmt.Errorf("work: input %q is required but value is null", spec.ID)
	}

	// Parse value schema if present.
	if len(spec.ValueSchema) > 0 {
		var hdr valueSchemaHeader
		if err := json.Unmarshal(spec.ValueSchema, &hdr); err != nil {
			return fmt.Errorf("work: input %q has invalid valueSchema JSON: %w", spec.ID, err)
		}
		if hdr.Kind != "" && InputKind(hdr.Kind) != spec.Kind {
			return fmt.Errorf("work: input %q valueSchema kind %q != spec kind %q", spec.ID, hdr.Kind, spec.Kind)
		}
	}

	// Kind-specific validation.
	switch spec.Kind {
	case InputText:
		return validateTextValue(spec, v)
	case InputNumber:
		return validateNumberValue(spec, v)
	case InputDate:
		return validateDateValue(spec, v)
	case InputChoice:
		return validateChoiceValue(spec, v)
	case InputMultiChoice:
		return validateMultiChoiceValue(spec, v)
	case InputRoster:
		return validateRosterValue(spec, v)
	case InputForm:
		return validateFormValue(spec, v)
	case InputFile:
		return validateFileValue(spec, v)
	case InputApproval:
		return validateApprovalValue(spec, v)
	default:
		return fmt.Errorf("work: unhandled InputKind %q", spec.Kind)
	}
}

// ── Kind-specific validators ────────────────────────────────────────────────

func validateTextValue(spec InputSpec, v json.RawMessage) error {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return fmt.Errorf("work: input %q (text) value must be a string: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c TextConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q text constraints invalid: %w", spec.ID, err)
	}
	runes := []rune(s)
	if c.MinLength > 0 && len(runes) < c.MinLength {
		return fmt.Errorf("work: input %q text too short: %d runes, min %d", spec.ID, len(runes), c.MinLength)
	}
	if c.MaxLength > 0 && len(runes) > c.MaxLength {
		return fmt.Errorf("work: input %q text too long: %d runes, max %d", spec.ID, len(runes), c.MaxLength)
	}
	if c.Pattern != "" {
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return fmt.Errorf("work: input %q text pattern invalid: %w", spec.ID, err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("work: input %q text does not match pattern %q", spec.ID, c.Pattern)
		}
	}
	return nil
}

func validateNumberValue(spec InputSpec, v json.RawMessage) error {
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return fmt.Errorf("work: input %q (number) value must be a number: %w", spec.ID, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("work: input %q number must be finite", spec.ID)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c NumberConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q number constraints invalid: %w", spec.ID, err)
	}
	if c.Min != nil && f < *c.Min {
		return fmt.Errorf("work: input %q number %v below min %v", spec.ID, f, *c.Min)
	}
	if c.Max != nil && f > *c.Max {
		return fmt.Errorf("work: input %q number %v above max %v", spec.ID, f, *c.Max)
	}
	if c.Integer && f != math.Trunc(f) {
		return fmt.Errorf("work: input %q number must be integer, got %v", spec.ID, f)
	}
	switch c.Unit {
	case "", "number", "amount":
	case "ratio":
		if f < 0 || f > 1 {
			return fmt.Errorf("work: input %q ratio must be 0..1", spec.ID)
		}
	case "percent":
		if f < 0 || f > 100 {
			return fmt.Errorf("work: input %q percent must be 0..100", spec.ID)
		}
	default:
		return fmt.Errorf("work: input %q number unit %q is invalid", spec.ID, c.Unit)
	}
	if c.Currency != "" && c.Unit != "amount" {
		return fmt.Errorf("work: input %q currency requires unit=amount", spec.ID)
	}
	if c.Currency != "" && !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(c.Currency) {
		return fmt.Errorf("work: input %q currency must be an ISO-style three-letter code", spec.ID)
	}
	return nil
}

func validateDateValue(spec InputSpec, v json.RawMessage) error {
	var c DateConstraints
	if len(spec.ValueSchema) > 0 {
		if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
			return fmt.Errorf("work: input %q date constraints invalid: %w", spec.ID, err)
		}
	}
	if c.Mode == "range" {
		var value struct {
			Start string `json:"start"`
			End   string `json:"end"`
		}
		if err := json.Unmarshal(v, &value); err != nil || value.Start == "" || value.End == "" {
			return fmt.Errorf("work: input %q date range requires non-empty start/end", spec.ID)
		}
		start, err := parseDateValue(value.Start, "")
		if err != nil {
			return fmt.Errorf("work: input %q range start: %w", spec.ID, err)
		}
		end, err := parseDateValue(value.End, "")
		if err != nil {
			return fmt.Errorf("work: input %q range end: %w", spec.ID, err)
		}
		if end.Before(start) {
			return fmt.Errorf("work: input %q range end precedes start", spec.ID)
		}
		return validateDateBounds(spec.ID, start, end, c)
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return fmt.Errorf("work: input %q (date) value must be a string: %w", spec.ID, err)
	}
	val, err := parseDateValue(s, c.Mode)
	if err != nil {
		return fmt.Errorf("work: input %q date: %w", spec.ID, err)
	}
	return validateDateBounds(spec.ID, val, val, c)
}

func validateDateBounds(inputID string, start, end time.Time, c DateConstraints) error {
	if c.MinDate != "" {
		minVal, err := parseDateConstraint(c.MinDate)
		if err != nil {
			return fmt.Errorf("work: input %q date minDate invalid: %w", inputID, err)
		}
		if start.Before(minVal) {
			return fmt.Errorf("work: input %q date before min", inputID)
		}
	}
	if c.MaxDate != "" {
		maxVal, err := parseDateConstraint(c.MaxDate)
		if err != nil {
			return fmt.Errorf("work: input %q date maxDate invalid: %w", inputID, err)
		}
		if end.After(maxVal) {
			return fmt.Errorf("work: input %q date after max", inputID)
		}
	}
	return nil
}

func parseDateValue(s, mode string) (time.Time, error) {
	switch mode {
	case "time":
		for _, layout := range []string{"15:04", "15:04:05"} {
			if value, err := time.Parse(layout, s); err == nil {
				return value, nil
			}
		}
		return time.Time{}, fmt.Errorf("time must be HH:MM or HH:MM:SS")
	case "date":
		return time.Parse("2006-01-02", s)
	case "", "datetime":
		if value, err := time.Parse(time.RFC3339, s); err == nil {
			return value, nil
		}
		if mode == "" {
			if value, err := time.Parse("2006-01-02", s); err == nil {
				return value, nil
			}
			for _, layout := range []string{"15:04", "15:04:05"} {
				if value, err := time.Parse(layout, s); err == nil {
					return value, nil
				}
			}
		}
		return time.Time{}, fmt.Errorf("datetime must be RFC3339")
	default:
		return time.Time{}, fmt.Errorf("mode %q is invalid", mode)
	}
}

func parseDateConstraint(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

func validateChoiceValue(spec InputSpec, v json.RawMessage) error {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return fmt.Errorf("work: input %q (choice) value must be a string: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c ChoiceConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q choice constraints invalid: %w", spec.ID, err)
	}
	if len(c.Options) == 0 {
		return fmt.Errorf("work: input %q choice requires at least one option", spec.ID)
	}
	for _, opt := range c.Options {
		if opt.Value == s {
			return nil
		}
	}
	if !c.AllowOther {
		return fmt.Errorf("work: input %q choice value %q not in options", spec.ID, s)
	}
	return nil
}

func validateMultiChoiceValue(spec InputSpec, v json.RawMessage) error {
	var arr []string
	if err := json.Unmarshal(v, &arr); err != nil {
		return fmt.Errorf("work: input %q (multi_choice) value must be a string array: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c MultiChoiceConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q multi_choice constraints invalid: %w", spec.ID, err)
	}
	if c.MinSelect > 0 && len(arr) < c.MinSelect {
		return fmt.Errorf("work: input %q multi_choice selected %d, min %d", spec.ID, len(arr), c.MinSelect)
	}
	if c.MaxSelect > 0 && len(arr) > c.MaxSelect {
		return fmt.Errorf("work: input %q multi_choice selected %d, max %d", spec.ID, len(arr), c.MaxSelect)
	}
	optSet := make(map[string]bool, len(c.Options))
	for _, opt := range c.Options {
		optSet[opt.Value] = true
	}
	for _, sel := range arr {
		if !optSet[sel] && !c.AllowOther {
			return fmt.Errorf("work: input %q multi_choice value %q not in options", spec.ID, sel)
		}
	}
	return nil
}

func validateRosterValue(spec InputSpec, v json.RawMessage) error {
	var arr []map[string]any
	if err := json.Unmarshal(v, &arr); err != nil {
		return fmt.Errorf("work: input %q (roster) value must be an array of objects: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c RosterConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q roster constraints invalid: %w", spec.ID, err)
	}
	if c.MinEntries > 0 && len(arr) < c.MinEntries {
		return fmt.Errorf("work: input %q roster entries %d, min %d", spec.ID, len(arr), c.MinEntries)
	}
	if c.MaxEntries > 0 && len(arr) > c.MaxEntries {
		return fmt.Errorf("work: input %q roster entries %d, max %d", spec.ID, len(arr), c.MaxEntries)
	}
	for i, entry := range arr {
		for _, field := range c.Fields {
			value, ok := entry[field]
			if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
				return fmt.Errorf("work: input %q roster[%d].%s is required", spec.ID, i, field)
			}
		}
	}
	return nil
}

func validateFormValue(spec InputSpec, v json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(v, &obj); err != nil {
		return fmt.Errorf("work: input %q (form) value must be an object: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c FormConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q form constraints invalid: %w", spec.ID, err)
	}
	for _, field := range c.Fields {
		fv, ok := obj[field.ID]
		if !ok || fv == nil {
			if field.Required {
				return fmt.Errorf("work: input %q form field %q is required", spec.ID, field.ID)
			}
			continue
		}
		fspec := InputSpec{
			ID:          spec.ID + "." + field.ID,
			Kind:        field.Kind,
			Required:    field.Required,
			ValueSchema: field.ValueSchema,
		}
		if err := ValidateInputValue(fspec, fv); err != nil {
			return fmt.Errorf("work: input %q form field %q: %w", spec.ID, field.ID, err)
		}
	}
	return nil
}

func validateFileValue(spec InputSpec, v json.RawMessage) error {
	// File value is a string (artifact ID) or array of strings.
	// Accept single string or string array.
	var single string
	if err := json.Unmarshal(v, &single); err == nil {
		if single == "" {
			return fmt.Errorf("work: input %q (file) value must be non-empty", spec.ID)
		}
		if len(spec.ValueSchema) > 0 {
			var c FileConstraints
			if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
				return fmt.Errorf("work: input %q file constraints invalid: %w", spec.ID, err)
			}
			if c.MaxCount < 0 {
				return fmt.Errorf("work: input %q file maxCount must be non-negative", spec.ID)
			}
		}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(v, &arr); err != nil {
		return fmt.Errorf("work: input %q (file) value must be a string or string array: %w", spec.ID, err)
	}
	if len(arr) == 0 {
		return fmt.Errorf("work: input %q (file) value must be non-empty", spec.ID)
	}
	for i, s := range arr {
		if s == "" {
			return fmt.Errorf("work: input %q file[%d] must be non-empty", spec.ID, i)
		}
	}
	if len(spec.ValueSchema) == 0 {
		return nil
	}
	var c FileConstraints
	if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
		return fmt.Errorf("work: input %q file constraints invalid: %w", spec.ID, err)
	}
	if c.MaxCount > 0 && len(arr) > c.MaxCount {
		return fmt.Errorf("work: input %q file count %d exceeds max %d", spec.ID, len(arr), c.MaxCount)
	}
	return nil
}

func validateApprovalValue(spec InputSpec, v json.RawMessage) error {
	// Approval value is a string: "approved" or "rejected".
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return fmt.Errorf("work: input %q (approval) value must be a string: %w", spec.ID, err)
	}
	if len(spec.ValueSchema) > 0 {
		var c ApprovalConstraints
		if err := json.Unmarshal(spec.ValueSchema, &c); err != nil {
			return fmt.Errorf("work: input %q approval constraints invalid: %w", spec.ID, err)
		}
		switch c.RiskLevel {
		case "", "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("work: input %q approval riskLevel %q is invalid", spec.ID, c.RiskLevel)
		}
	}
	switch strings.ToLower(s) {
	case "approved", "rejected":
		return nil
	default:
		return fmt.Errorf("work: input %q approval value must be \"approved\" or \"rejected\", got %q", spec.ID, s)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// isEmptyJSON returns true when v is null, "null", "", [], or {}.
func isEmptyJSON(v json.RawMessage) bool {
	if len(v) == 0 {
		return true
	}
	s := strings.TrimSpace(string(v))
	switch s {
	case "null", `""`, "[]", "{}", "":
		return true
	default:
		return false
	}
}
