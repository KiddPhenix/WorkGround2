package work

import (
	"encoding/json"
	"testing"
)

func mustJSONRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(b)
}

func TestValidateInputValue_RequiredEmpty(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, Required: true}
	if err := ValidateInputValue(spec, nil); err == nil {
		t.Fatal("nil value should fail for required input")
	}
	if err := ValidateInputValue(spec, mustJSONRaw(nil)); err == nil {
		t.Fatal("null value should fail for required input")
	}
	if err := ValidateInputValue(spec, mustJSONRaw("")); err == nil {
		t.Fatal("empty string should fail for required input")
	}
}

func TestValidateInputValue_UnknownKind(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: "bogus"}
	if err := ValidateInputValue(spec, mustJSONRaw("x")); err == nil {
		t.Fatal("unknown kind should fail")
	}
}

func TestValidateInputValue_BadSchema(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, ValueSchema: json.RawMessage("not-json")}
	if err := ValidateInputValue(spec, mustJSONRaw("x")); err == nil {
		t.Fatal("bad valueSchema JSON should fail")
	}
}

func TestValidateInputValue_SchemaKindMismatch(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, ValueSchema: mustJSONRaw(map[string]any{"kind": "number"})}
	if err := ValidateInputValue(spec, mustJSONRaw("x")); err == nil {
		t.Fatal("kind mismatch should fail")
	}
}

func TestValidateInputValue_OptionalNull(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, Required: false}
	if err := ValidateInputValue(spec, nil); err != nil {
		t.Fatalf("nil value for optional input should pass: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(nil)); err != nil {
		t.Fatalf("null value for optional input should pass: %v", err)
	}
}

// ── Text ────────────────────────────────────────────────────────────────────

func TestValidateTextValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText}
	if err := ValidateInputValue(spec, mustJSONRaw("hello")); err != nil {
		t.Fatalf("valid text: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(42)); err == nil {
		t.Fatal("number should not be valid text")
	}
}

func TestValidateTextValue_Constraints(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, ValueSchema: mustJSONRaw(TextConstraints{MinLength: 2, MaxLength: 5})}
	if err := ValidateInputValue(spec, mustJSONRaw("ab")); err != nil {
		t.Fatalf("min length ok: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("abcde")); err != nil {
		t.Fatalf("max length ok: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("a")); err == nil {
		t.Fatal("too short should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw("abcdef")); err == nil {
		t.Fatal("too long should fail")
	}
}

func TestValidateTextValue_Pattern(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputText, ValueSchema: mustJSONRaw(TextConstraints{Pattern: "^[a-z]+$"})}
	if err := ValidateInputValue(spec, mustJSONRaw("abc")); err != nil {
		t.Fatalf("pattern match: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("ABC")); err == nil {
		t.Fatal("pattern mismatch should fail")
	}
}

func TestValidateTextValue_Multiline(t *testing.T) {
	spec := InputSpec{
		ID:          "word-list",
		Kind:        InputText,
		ValueSchema: mustJSONRaw(TextConstraints{Multiline: true}),
	}
	if err := ValidateInputValue(spec, mustJSONRaw("hello\nworld")); err != nil {
		t.Fatalf("multiline text should remain a valid string: %v", err)
	}
}

// ── Number ──────────────────────────────────────────────────────────────────

func TestValidateNumberValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputNumber}
	if err := ValidateInputValue(spec, mustJSONRaw(3.14)); err != nil {
		t.Fatalf("valid number: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("not-number")); err == nil {
		t.Fatal("string should not be valid number")
	}
}

func TestValidateNumberValue_NaN(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputNumber}
	// encoding/json cannot marshal NaN — test via raw message
	if err := ValidateInputValue(spec, mustJSONRaw("NaN")); err == nil {
		t.Fatal("NaN string should fail")
	}
}

func TestValidateNumberValue_ConstraintHintsDoNotBlock(t *testing.T) {
	minv := 0.0
	maxv := 100.0
	spec := InputSpec{ID: "in", Kind: InputNumber, ValueSchema: mustJSONRaw(NumberConstraints{Min: &minv, Max: &maxv})}
	if err := ValidateInputValue(spec, mustJSONRaw(50.0)); err != nil {
		t.Fatalf("in range: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(0.0)); err != nil {
		t.Fatalf("at min: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(100.0)); err != nil {
		t.Fatalf("at max: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(-1.0)); err != nil {
		t.Fatalf("below min remains submittable: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(101.0)); err != nil {
		t.Fatalf("above max remains submittable: %v", err)
	}
}

func TestValidateNumberValue_Integer(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputNumber, ValueSchema: mustJSONRaw(NumberConstraints{Integer: true})}
	if err := ValidateInputValue(spec, mustJSONRaw(42.0)); err != nil {
		t.Fatalf("integer 42: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(3.14)); err != nil {
		t.Fatalf("integer hint must not block a decimal: %v", err)
	}
}

func TestValidateNumberValue_AmountAndRatio(t *testing.T) {
	amount := InputSpec{ID: "price", Kind: InputNumber, Required: true, ValueSchema: json.RawMessage(`{"kind":"number","unit":"amount","currency":"USD"}`)}
	if err := ValidateInputValue(amount, json.RawMessage(`12.5`)); err != nil {
		t.Fatal(err)
	}
	ratio := InputSpec{ID: "ratio", Kind: InputNumber, Required: true, ValueSchema: json.RawMessage(`{"kind":"number","unit":"ratio"}`)}
	if err := ValidateInputValue(ratio, json.RawMessage(`1.1`)); err != nil {
		t.Fatalf("ratio hint must not block submission: %v", err)
	}
	percent := InputSpec{ID: "percent", Kind: InputNumber, Required: true, ValueSchema: json.RawMessage(`{"kind":"number","unit":"percent"}`)}
	if err := ValidateInputValue(percent, json.RawMessage(`75`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputValue(percent, json.RawMessage(`101`)); err != nil {
		t.Fatalf("percent hint must not block submission: %v", err)
	}
	badCurrency := amount
	badCurrency.ValueSchema = json.RawMessage(`{"kind":"number","unit":"amount","currency":"usd"}`)
	if err := ValidateInputValue(badCurrency, json.RawMessage(`1`)); err == nil {
		t.Fatal("invalid currency code must fail")
	}
}

// ── Date ────────────────────────────────────────────────────────────────────

func TestValidateDateValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputDate}
	if err := ValidateInputValue(spec, mustJSONRaw("2026-07-23")); err != nil {
		t.Fatalf("valid date: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("2026-07-23T10:15:00Z")); err != nil {
		t.Fatalf("valid RFC3339: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("not-a-date")); err == nil {
		t.Fatal("invalid date should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw(42)); err == nil {
		t.Fatal("number should not be valid date")
	}
}

func TestValidateDateValue_Range(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputDate, ValueSchema: mustJSONRaw(DateConstraints{
		MinDate: "2026-01-01",
		MaxDate: "2026-12-31",
	})}
	if err := ValidateInputValue(spec, mustJSONRaw("2026-06-15")); err != nil {
		t.Fatalf("in range: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("2025-12-31")); err == nil {
		t.Fatal("before min should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw("2027-01-01")); err == nil {
		t.Fatal("after max should fail")
	}
}

func TestValidateDateValue_TimeAndTimeRange(t *testing.T) {
	timeSpec := InputSpec{ID: "time", Kind: InputDate, Required: true, ValueSchema: json.RawMessage(`{"kind":"date","mode":"time"}`)}
	if err := ValidateInputValue(timeSpec, json.RawMessage(`"09:30"`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputValue(timeSpec, json.RawMessage(`"25:00"`)); err == nil {
		t.Fatal("invalid time must fail")
	}
	dateTimeSpec := InputSpec{ID: "datetime", Kind: InputDate, Required: true, ValueSchema: json.RawMessage(`{"kind":"date","mode":"datetime"}`)}
	if err := ValidateInputValue(dateTimeSpec, json.RawMessage(`"2026-07-24T09:30:00Z"`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputValue(dateTimeSpec, json.RawMessage(`"2026-07-24 09:30"`)); err == nil {
		t.Fatal("non-RFC3339 datetime must fail")
	}
	rangeSpec := InputSpec{ID: "range", Kind: InputDate, Required: true, ValueSchema: json.RawMessage(`{"kind":"date","mode":"range"}`)}
	if err := ValidateInputValue(rangeSpec, json.RawMessage(`{"start":"09:00","end":"10:30"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputValue(rangeSpec, json.RawMessage(`{"start":"10:30","end":"09:00"}`)); err == nil {
		t.Fatal("reversed time range must fail")
	}
}

// ── Choice ──────────────────────────────────────────────────────────────────

func TestValidateChoiceValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputChoice}
	if err := ValidateInputValue(spec, mustJSONRaw("any")); err != nil {
		t.Fatalf("valid choice without schema: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(42)); err == nil {
		t.Fatal("number should not be valid choice")
	}
}

func TestValidateChoiceValue_Options(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputChoice, ValueSchema: mustJSONRaw(ChoiceConstraints{
		Options: []ChoiceOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}},
	})}
	if err := ValidateInputValue(spec, mustJSONRaw("a")); err != nil {
		t.Fatalf("valid choice: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("c")); err == nil {
		t.Fatal("unknown option should fail")
	}
}

func TestValidateChoiceValue_AllowOther(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputChoice, ValueSchema: mustJSONRaw(ChoiceConstraints{
		Options:    []ChoiceOption{{Value: "a", Label: "A"}},
		AllowOther: true,
	})}
	if err := ValidateInputValue(spec, mustJSONRaw("custom")); err != nil {
		t.Fatalf("allowOther: %v", err)
	}
}

func TestNormalizeInputSpecValueSchema_RecoversLegacyChoiceOptions(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{"object map", `{"options":{"visual":"视觉学习","audio":"听觉学习"}}`},
		{"delimited string", `{"options":"visual, audio"}`},
		{"string array", `{"options":["visual","audio"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := InputSpec{ID: "method", Kind: InputChoice, ValueSchema: json.RawMessage(tt.schema)}
			normalized, err := NormalizeInputSpecValueSchema(spec)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			var constraints ChoiceConstraints
			if err := json.Unmarshal(normalized, &constraints); err != nil {
				t.Fatalf("decode normalized: %v", err)
			}
			if len(constraints.Options) != 2 {
				t.Fatalf("options = %#v", constraints.Options)
			}
			spec.ValueSchema = normalized
			if err := ValidateInputValue(spec, mustJSONRaw("visual")); err != nil {
				t.Fatalf("validate normalized choice: %v", err)
			}
		})
	}
}

func TestValidateChoiceValue_RecoversLegacyOptionsAtSubmit(t *testing.T) {
	spec := InputSpec{
		ID: "method", Kind: InputChoice,
		ValueSchema: json.RawMessage(`{"options":{"visual":"视觉学习","audio":"听觉学习"}}`),
	}
	if err := ValidateInputValue(spec, mustJSONRaw("visual")); err != nil {
		t.Fatalf("legacy object options should remain submit-compatible: %v", err)
	}
}

// ── MultiChoice ─────────────────────────────────────────────────────────────

func TestValidateMultiChoiceValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputMultiChoice}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "b"})); err != nil {
		t.Fatalf("valid multi_choice: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("a")); err == nil {
		t.Fatal("string should not be valid multi_choice")
	}
}

func TestValidateMultiChoiceValue_Constraints(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputMultiChoice, ValueSchema: mustJSONRaw(MultiChoiceConstraints{
		Options:   []ChoiceOption{{Value: "a"}, {Value: "b"}, {Value: "c"}},
		MinSelect: 1,
		MaxSelect: 2,
	})}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "b"})); err != nil {
		t.Fatalf("valid selection: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{})); err == nil {
		t.Fatal("zero selection below min should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "b", "c"})); err == nil {
		t.Fatal("too many selections should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"x"})); err == nil {
		t.Fatal("unknown option should fail")
	}
}

func TestValidateMultiChoiceValue_AllowOther(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputMultiChoice, ValueSchema: mustJSONRaw(MultiChoiceConstraints{
		Options:    []ChoiceOption{{Value: "a"}},
		AllowOther: true,
	})}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "custom"})); err != nil {
		t.Fatalf("allowOther: %v", err)
	}
}

// ── Roster ──────────────────────────────────────────────────────────────────

func TestValidateRosterValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputRoster}
	if err := ValidateInputValue(spec, mustJSONRaw([]map[string]any{{"name": "Alice"}})); err != nil {
		t.Fatalf("valid roster: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("bad")); err == nil {
		t.Fatal("string should not be valid roster")
	}
}

func TestValidateRosterValue_Constraints(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputRoster, ValueSchema: mustJSONRaw(RosterConstraints{MinEntries: 1, MaxEntries: 3})}
	if err := ValidateInputValue(spec, mustJSONRaw([]map[string]any{{"name": "A"}, {"name": "B"}})); err != nil {
		t.Fatalf("valid roster: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]map[string]any{})); err == nil {
		t.Fatal("empty roster below min should fail")
	}
}

func TestValidateRosterValue_RequiredIdentityFields(t *testing.T) {
	spec := InputSpec{ID: "owners", Kind: InputRoster, Required: true, ValueSchema: json.RawMessage(`{"kind":"roster","fields":["user","team","role"]}`)}
	if err := ValidateInputValue(spec, json.RawMessage(`[{"user":"u1","team":"t1","role":"owner"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputValue(spec, json.RawMessage(`[{"user":"u1","team":"t1"}]`)); err == nil {
		t.Fatal("missing role must fail")
	}
}

// ── Form ────────────────────────────────────────────────────────────────────

func TestValidateFormValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputForm}
	if err := ValidateInputValue(spec, mustJSONRaw(map[string]any{"field1": "val"})); err != nil {
		t.Fatalf("valid form: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("bad")); err == nil {
		t.Fatal("string should not be valid form")
	}
}

func TestValidateFormValue_NestedValidation(t *testing.T) {
	spec := InputSpec{ID: "f", Kind: InputForm, ValueSchema: mustJSONRaw(FormConstraints{
		Fields: []FormFieldSpec{
			{ID: "name", Kind: InputText, Required: true},
			{ID: "count", Kind: InputNumber, ValueSchema: mustJSONRaw(NumberConstraints{Integer: true})},
		},
	})}
	if err := ValidateInputValue(spec, mustJSONRaw(map[string]any{"name": "test", "count": 5})); err != nil {
		t.Fatalf("valid nested form: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw(map[string]any{"count": 5})); err == nil {
		t.Fatal("missing required field should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw(map[string]any{"name": "test", "count": 3.5})); err == nil {
		t.Fatal("non-integer should fail")
	}
}

// ── File/Artifact ───────────────────────────────────────────────────────────

func TestValidateFileValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputFile}
	if err := ValidateInputValue(spec, mustJSONRaw("artifact-id")); err != nil {
		t.Fatalf("single file: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a1", "a2"})); err != nil {
		t.Fatalf("file array: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("")); err == nil {
		t.Fatal("empty string should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{})); err == nil {
		t.Fatal("empty array should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw(42)); err == nil {
		t.Fatal("number should not be valid file")
	}
}

func TestValidateFileValue_MaxCount(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputFile, ValueSchema: mustJSONRaw(FileConstraints{MaxCount: 2})}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "b"})); err != nil {
		t.Fatalf("at max: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw([]string{"a", "b", "c"})); err == nil {
		t.Fatal("too many files should fail")
	}
}

// ── Approval ────────────────────────────────────────────────────────────────

func TestValidateApprovalValue_Basic(t *testing.T) {
	spec := InputSpec{ID: "in", Kind: InputApproval}
	if err := ValidateInputValue(spec, mustJSONRaw("approved")); err != nil {
		t.Fatalf("approved: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("rejected")); err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("APPROVED")); err != nil {
		t.Fatalf("case insensitive: %v", err)
	}
	if err := ValidateInputValue(spec, mustJSONRaw("maybe")); err == nil {
		t.Fatal("unknown approval value should fail")
	}
	if err := ValidateInputValue(spec, mustJSONRaw(42)); err == nil {
		t.Fatal("number should not be valid approval")
	}
}

func TestValidateApprovalValue_Risk(t *testing.T) {
	spec := InputSpec{ID: "risk", Kind: InputApproval, Required: true, ValueSchema: json.RawMessage(`{"kind":"approval","riskLevel":"critical"}`)}
	if err := ValidateInputValue(spec, json.RawMessage(`"approved"`)); err != nil {
		t.Fatal(err)
	}
	spec.ValueSchema = json.RawMessage(`{"kind":"approval","riskLevel":"surprise"}`)
	if err := ValidateInputValue(spec, json.RawMessage(`"approved"`)); err == nil {
		t.Fatal("unknown risk level must fail")
	}
}

// ── Empty check helper ──────────────────────────────────────────────────────

func TestIsEmptyJSON(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"null", true},
		{`""`, true},
		{"[]", true},
		{"{}", true},
		{`"hello"`, false},
		{"[1]", false},
		{`{"a":1}`, false},
		{"0", false},
	}
	for _, tt := range tests {
		got := isEmptyJSON(json.RawMessage(tt.input))
		if got != tt.want {
			t.Errorf("isEmptyJSON(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
