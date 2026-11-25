package protocol

// P13-01 acceptance: the E3 checks are tested as the refusal vocabulary they
// are — one case per reason this unit can produce, plus an enumeration of the
// contract's closed enums so a new reason cannot be added untested.

import (
	"reflect"
	"testing"
)

// TestStrictDecodeRefusesDuplicateField is the selector-smuggling primitive:
// Go keeps the last duplicate silently, so {"context":"x11::0","context":"wts:7"}
// would audit one context and act in the other.
func TestStrictDecodeRefusesDuplicateField(t *testing.T) {
	cases := []string{
		`{"context":"x11::0","context":"wts:7"}`,
		`{"session":3,"session":9}`,
		`{"text":"a","text":"b","encoding":"unicode"}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			_, serr := ParseStrictBytes[InputParams]([]byte(body), AllowTextInput)
			if serr == nil {
				t.Fatalf("duplicate top-level key accepted: %s", body)
			}
			if serr.Reason != ReasonDuplicateField {
				t.Fatalf("reason = %q, want %q", serr.Reason, ReasonDuplicateField)
			}
			if serr.Field == "" {
				t.Fatal("detail.field is empty; the refusal must name the duplicated key")
			}
		})
	}
}

// TestStrictDecodeRefusesUnknownField: each route's own key set is what
// rejects a field the type knows but the route does not accept.
func TestStrictDecodeRefusesUnknownField(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		allowed []string
		want    string
	}{
		{"click-with-text", `{"x":1,"y":2,"text":"abc"}`, AllowPointerClick, "text"},
		{"text-with-keys", `{"keys":["Ctrl","S"]}`, AllowTextInput, "keys"},
		{"capture-with-display", `{"display":0}`, AllowPointerClick, "display"},
		{"exec-with-session-typo", `{"command":"id","sesion":1}`, AllowExec, "sesion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, serr := ParseStrictBytes[InputParams]([]byte(tc.body), tc.allowed)
			if serr == nil {
				t.Fatalf("unknown key accepted for this route: %s", tc.body)
			}
			if serr.Reason != ReasonUnknownField {
				t.Fatalf("reason = %q, want %q", serr.Reason, ReasonUnknownField)
			}
			if serr.Field != tc.want {
				t.Fatalf("field = %q, want %q", serr.Field, tc.want)
			}
			if len(serr.Allowed) == 0 {
				t.Fatal("detail.allowed is empty; the caller cannot see what the route accepts")
			}
		})
	}
}

// TestStrictDecodeRefusesTypeMismatch covers contract C-5: capture with a
// numeric window must be a typed 400, not a dropped key.
func TestStrictDecodeRefusesTypeMismatch(t *testing.T) {
	_, serr := ParseStrictBytes[ScreenParams]([]byte(`{"window":66102}`), AllowCapture)
	if serr == nil {
		t.Fatal("numeric window accepted; contract types window as string")
	}
	if serr.Reason != ReasonTypeMismatch {
		t.Fatalf("reason = %q, want %q", serr.Reason, ReasonTypeMismatch)
	}
	if serr.Field != "window" || serr.Want != "string" || serr.Got != "number" {
		t.Fatalf("field/want/got = %q/%q/%q, want window/string/number", serr.Field, serr.Want, serr.Got)
	}
}

// TestStrictDecodeAcceptsAllowListedKeys is the control: the helper is not a
// blanket refusal, and nested object keys are not mistaken for top-level ones.
func TestStrictDecodeAcceptsAllowListedKeys(t *testing.T) {
	body := `{"context":"x11::0","window":"0x3c00007","rect":{"x":1,"y":2,"w":10,"h":10},"format":"png","hash_only":true}`
	params, serr := ParseStrictBytes[ScreenParams]([]byte(body), AllowCapture)
	if serr != nil {
		t.Fatalf("documented keys refused: %v", serr)
	}
	if params.Window != "0x3c00007" || params.Rect == nil || params.Rect.W != 10 || !params.HashOnly {
		t.Fatalf("params not decoded: %+v", params)
	}
}

// TestParseCommandStrictRefusesSmuggledField is E3b's mechanism: a bypassed or
// old server cannot smuggle a field to the platform — the agent's own parse
// refuses it before any handler touches the platform.
func TestParseCommandStrictRefusesSmuggledField(t *testing.T) {
	env := Envelope{ID: "1", Type: TypeTextInput, Params: []byte(`{"text":"a","context":"wts:7"}`)}
	if _, serr := ParseCommandStrict[InputParams](env, AllowTextInput); serr != nil {
		t.Fatalf("allow-listed selector refused on the agent side: %v", serr)
	}
	env.Params = []byte(`{"text":"a","session":"7"}`)
	if _, serr := ParseCommandStrict[InputParams](env, AllowTextInput); serr == nil || serr.Reason != ReasonTypeMismatch {
		t.Fatalf("smuggled session not refused with TYPE_MISMATCH: %+v", serr)
	}
	env.Params = []byte(`{"text":"a","display":0}`)
	if _, serr := ParseCommandStrict[InputParams](env, AllowTextInput); serr == nil || serr.Reason != ReasonUnknownField {
		t.Fatalf("smuggled display not refused with UNKNOWN_FIELD: %+v", serr)
	}
}

// TestInvalidParamRefusalProducers is the per-reason test the acceptance asks
// for: every reason this unit can emit is produced here by its real code path.
func TestInvalidParamRefusalProducers(t *testing.T) {
	produce := map[string]func() *StrictError{
		ReasonUnknownField: func() *StrictError {
			_, e := ParseStrictBytes[InputParams]([]byte(`{"display":0}`), AllowPointerClick)
			return e
		},
		ReasonTypeMismatch: func() *StrictError {
			_, e := ParseStrictBytes[ScreenParams]([]byte(`{"quality":"high"}`), AllowCapture)
			return e
		},
		ReasonDuplicateField: func() *StrictError {
			_, e := ParseStrictBytes[InputParams]([]byte(`{"context":"x11::0","context":"wts:7"}`), AllowPointerClick)
			return e
		},
		ReasonOutOfRange: func() *StrictError {
			s := 70000
			return ValidateSelector(nil, &s)
		},
		ReasonSelectorConflict: func() *StrictError {
			c, s := "x11::0", 7
			return ValidateSelector(&c, &s)
		},
		ReasonStreamIDRequired: func() *StrictError {
			return RequireField("stream_id", ReasonStreamIDRequired, AllowStreamStop)
		},
		ReasonContextNotAccepted: func() *StrictError {
			return RefuseSelector(AllowStreamStop)
		},
	}
	if len(produce) != len(StrictProducedReasons) {
		t.Fatalf("producers = %d, StrictProducedReasons = %d: every produced reason needs a test",
			len(produce), len(StrictProducedReasons))
	}
	for _, reason := range StrictProducedReasons {
		fn, ok := produce[reason]
		if !ok {
			t.Fatalf("reason %q is declared as produced but has no producer test", reason)
		}
		t.Run(reason, func(t *testing.T) {
			serr := fn()
			if serr == nil {
				t.Fatalf("producer for %q returned no refusal", reason)
			}
			if serr.Reason != reason {
				t.Fatalf("reason = %q, want %q", serr.Reason, reason)
			}
		})
	}
}

// TestInvalidParamReasonEnumIsClosed pins the §5.5 enum verbatim. A reason that
// is not in the contract, or a contract reason silently dropped, fails here.
func TestInvalidParamReasonEnumIsClosed(t *testing.T) {
	want := []string{
		"UNKNOWN_FIELD", "TYPE_MISMATCH", "DUPLICATE_FIELD", "MISSING_FIELD",
		"OUT_OF_RANGE", "SELECTOR_CONFLICT", "CONTEXT_NOT_ACCEPTED",
		"DETACH_WITH_CONTEXT_UNSUPPORTED", "STREAM_ID_REQUIRED",
		"REGION_TOO_LARGE", "ALIAS_KIND_NOT_APPLICABLE",
	}
	if !reflect.DeepEqual(InvalidParamReasons, want) {
		t.Fatalf("InvalidParamReasons drifted from §5.5:\n got %v\nwant %v", InvalidParamReasons, want)
	}
	declared := make(map[string]bool, len(want))
	for _, r := range want {
		declared[r] = true
	}
	for _, r := range StrictProducedReasons {
		if !declared[r] {
			t.Fatalf("produced reason %q is not in the contract enum", r)
		}
	}
}

// TestContextUnavailableEnumIsClosed pins the §5.4 enum. This unit defines the
// vocabulary only: nothing here resolves a context, so nothing fakes a refusal
// it cannot produce.
func TestContextUnavailableEnumIsClosed(t *testing.T) {
	want := []string{
		"NOT_FOUND", "NOT_INTERACTIVE", "NO_USER", "OTHER_USER",
		"PRIVILEGE_NOT_HELD", "ENTER_FAILED", "GONE",
		"KIND_NOT_APPLICABLE", "NO_BACKEND",
	}
	if !reflect.DeepEqual(ContextUnavailableReasons, want) {
		t.Fatalf("ContextUnavailableReasons drifted from §5.4:\n got %v\nwant %v", ContextUnavailableReasons, want)
	}
}
