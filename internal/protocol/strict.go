package protocol

// P13-01 (contract P10-07 v3.1 §5.5). The three strict-decode checks live here
// once, so the server (E3a) and the agent (E3b) cannot drift: a body is walked
// for duplicate and unknown top-level keys before it is decoded with
// DisallowUnknownFields. Go's encoding/json keeps the *last* duplicate
// silently, which is the selector-smuggling primitive E3c closes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Closed reason enums from the contract. A reason that is not listed here is a
// defect, not an extension: tests enumerate these slices.
const (
	ReasonUnknownField     = "UNKNOWN_FIELD"
	ReasonTypeMismatch     = "TYPE_MISMATCH"
	ReasonDuplicateField   = "DUPLICATE_FIELD"
	ReasonMissingField     = "MISSING_FIELD"
	ReasonOutOfRange       = "OUT_OF_RANGE"
	ReasonSelectorConflict = "SELECTOR_CONFLICT"
	ReasonContextNotAccepted = "CONTEXT_NOT_ACCEPTED"
	ReasonStreamIDRequired = "STREAM_ID_REQUIRED"
)

// InvalidParamReasons is the §5.5 INVALID_PARAMS reason enum, verbatim.
// DETACH_WITH_CONTEXT_UNSUPPORTED, REGION_TOO_LARGE and
// ALIAS_KIND_NOT_APPLICABLE are declared but have no producer in this unit
// (detach+context, capture region limits and the wts alias live in later
// units); StrictProducedReasons names what this substrate can emit.
var InvalidParamReasons = []string{
	ReasonUnknownField, ReasonTypeMismatch, ReasonDuplicateField,
	ReasonMissingField, ReasonOutOfRange, ReasonSelectorConflict,
	ReasonContextNotAccepted, "DETACH_WITH_CONTEXT_UNSUPPORTED",
	ReasonStreamIDRequired, "REGION_TOO_LARGE", "ALIAS_KIND_NOT_APPLICABLE",
}

// StrictProducedReasons is the subset this unit's code paths can return.
// Every entry here has a test; anything added here without one is a defect.
var StrictProducedReasons = []string{
	ReasonUnknownField, ReasonTypeMismatch, ReasonDuplicateField,
	ReasonOutOfRange, ReasonSelectorConflict,
	ReasonContextNotAccepted, ReasonStreamIDRequired,
}

// RequireField is the required-key refusal (§5.5 row 4): the key was absent or
// empty. Shared so the server and the agent refuse it with one shape.
func RequireField(field, reason string, allowed []string) *StrictError {
	return &StrictError{
		Reason:  reason,
		Field:   field,
		Allowed: allowed,
		Message: fmt.Sprintf("field %q is required", field),
	}
}

// RefuseSelector is the §5.6 rule for a route that accepts a selector only so
// it can refuse it (stream-stop): a stream is stopped by its id.
func RefuseSelector(allowed []string) *StrictError {
	return &StrictError{
		Reason:  ReasonContextNotAccepted,
		Field:   "context",
		Allowed: allowed,
		Message: "this route does not accept a context selector",
	}
}

// ContextUnavailableReasons is the §5.4 closed enum. This unit defines the
// vocabulary; the context-entry unit is the one that can produce these (no
// resolution exists yet, so nothing here fakes one).
var ContextUnavailableReasons = []string{
	"NOT_FOUND", "NOT_INTERACTIVE", "NO_USER", "OTHER_USER",
	"PRIVILEGE_NOT_HELD", "ENTER_FAILED", "GONE",
	"KIND_NOT_APPLICABLE", "NO_BACKEND",
}

// RouteAllowLists is the §5.5 table as data: one list per route/handler. The
// server (E3a) and the agent (E3b) both read these exact slices, so the two
// sides cannot disagree about what a route accepts.
var (
	AllowExec = []string{
		"command", "timeout", "workdir", "env", "as", "detach", "credential",
		"credential_mode", "job_id", "on_death", "job_timeout_s",
		"idempotency_key", "context", "session",
	}
	AllowCapture = []string{
		"context", "session", "display", "window", "rect", "format", "quality",
		"hash_only", "since",
	}
	AllowStreamStart = []string{"context", "session", "display", "fps", "quality"}
	AllowStreamStop  = []string{"stream_id", "context", "session"}
	AllowStreamFrame = []string{}
	AllowPointerMove = []string{"context", "session", "x", "y", "window"}
	AllowPointerClick = []string{
		"context", "session", "x", "y", "button", "count", "window", "focus",
	}
	AllowPointerScroll = []string{
		"context", "session", "x", "y", "delta", "unit", "window",
	}
	AllowPointerDrag = []string{
		"context", "session", "from", "to", "button", "duration_ms", "steps", "window",
	}
	AllowKeyPress  = []string{"context", "session", "key", "window", "focus"}
	AllowKeyCombo  = []string{"context", "session", "keys", "window", "focus"}
	AllowTextInput = []string{"context", "session", "text", "encoding", "window", "focus"}
	AllowUITree    = []string{"context", "session", "visible_only", "window", "max"}
	AllowUIWindow  = []string{"context", "session", "window", "action", "x", "y", "w", "h"}

	// AllowContextsQuery is §6's GET: it enumerates, it does not act, so it
	// carries no selector and accepts no query key at all. It exists as a
	// named empty list so the refusal can print `detail.allowed: []` instead of
	// leaving a caller to guess whether some key was legal.
	AllowContextsQuery = []string{}
)

// StrictError is a refusal decided before any dispatch: it names the contract
// reason and the field, never a free-text decode error.
type StrictError struct {
	Reason  string
	Field   string
	Allowed []string
	Want    string
	Got     string
	Message string
}

func (e *StrictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Field != "" {
		return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
	}
	return e.Reason
}

// SelectorCarrier is implemented by every selector-bearing param type, so the
// server can gate on selector presence without knowing the concrete type.
type SelectorCarrier interface {
	Selector() (*string, *int)
}

// ParseStrictBytes applies the three E3 checks to a raw body: duplicate
// top-level keys, allow-list membership, then a strict struct decode.
func ParseStrictBytes[T any](raw []byte, allowed []string) (T, *StrictError) {
	var out T
	body := bytes.TrimSpace(raw)
	if len(body) == 0 {
		body = []byte("{}")
	}

	keys, serr := topLevelKeys(body)
	if serr != nil {
		return out, serr
	}

	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			return out, &StrictError{
				Reason:  ReasonDuplicateField,
				Field:   k,
				Allowed: allowed,
				Message: fmt.Sprintf("field %q appears twice", k),
			}
		}
		seen[k] = true
	}
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	for _, k := range keys {
		if !allow[k] {
			return out, &StrictError{
				Reason:  ReasonUnknownField,
				Field:   k,
				Allowed: allowed,
				Message: fmt.Sprintf("field %q is not accepted by this route", k),
			}
		}
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, classifyDecodeError(err)
	}
	return out, nil
}

// ParseCommandStrict is the agent-side entry point: the same three checks on
// an envelope's params, so a bypassed or old server cannot smuggle a field.
func ParseCommandStrict[T any](env Envelope, allowed []string) (T, *StrictError) {
	return ParseStrictBytes[T](env.Params, allowed)
}

// topLevelKeys walks the body as JSON tokens and returns the object's keys in
// order. A body that is not a JSON object is a TYPE_MISMATCH (the body itself
// has the wrong type), never a silent {}.
func topLevelKeys(body []byte) ([]string, *StrictError) {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, &StrictError{
			Reason:  ReasonTypeMismatch,
			Want:    "object",
			Got:     "malformed",
			Message: "request body is not valid JSON: " + err.Error(),
		}
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return nil, &StrictError{
			Reason:  ReasonTypeMismatch,
			Want:    "object",
			Got:     tokenKind(tok),
			Message: "request body must be a JSON object",
		}
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, &StrictError{Reason: ReasonTypeMismatch, Want: "object", Got: "malformed",
				Message: "request body is not valid JSON: " + err.Error()}
		}
		key, ok := kt.(string)
		if !ok {
			return nil, &StrictError{Reason: ReasonTypeMismatch, Want: "object", Got: "malformed",
				Message: "request body is not a JSON object"}
		}
		keys = append(keys, key)
		if err := skipValue(dec); err != nil {
			return nil, &StrictError{Reason: ReasonTypeMismatch, Want: "object", Got: "malformed",
				Message: "request body is not valid JSON: " + err.Error()}
		}
	}
	return keys, nil
}

// skipValue consumes one JSON value from the token stream, including nested
// objects and arrays, so key walking never confuses a nested key with a
// top-level one.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if dd, ok := t.(json.Delim); ok {
			if dd == '{' || dd == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

func tokenKind(tok json.Token) string {
	switch v := tok.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case json.Delim:
		return "object"
	default:
		_ = v
		return "unknown"
	}
}

// classifyDecodeError maps a decode failure onto the contract's closed enum:
// a wrong JSON type for a known key is TYPE_MISMATCH with field/want/got.
func classifyDecodeError(err error) *StrictError {
	if te, ok := err.(*json.UnmarshalTypeError); ok {
		want := wireType(te.Type)
		got := te.Value
		if got == "" {
			got = "null"
		}
		return &StrictError{
			Reason:  ReasonTypeMismatch,
			Field:   te.Field,
			Want:    want,
			Got:     got,
			Message: fmt.Sprintf("field %q must be a %s", te.Field, want),
		}
	}
	if se, ok := err.(*json.SyntaxError); ok {
		return &StrictError{Reason: ReasonTypeMismatch, Want: "object", Got: "malformed",
			Message: "request body is not valid JSON: " + se.Error()}
	}
	if strings.HasPrefix(err.Error(), "json: unknown field") {
		return &StrictError{Reason: ReasonUnknownField, Message: err.Error()}
	}
	return &StrictError{Reason: ReasonTypeMismatch, Want: "object", Got: "malformed", Message: err.Error()}
}

// wireType names a Go field type the way the contract speaks about JSON.
func wireType(t reflect.Type) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map, reflect.Ptr, reflect.Interface:
		return "object"
	default:
		return t.Kind().String()
	}
}

// ValidateSelector enforces §5.1 where the selector is parsed, before any
// syscall: an out-of-range session is OUT_OF_RANGE, and a context that
// disagrees with the wts alias is SELECTOR_CONFLICT. Range is checked first
// because a value that cannot name a session cannot be compared with one.
func ValidateSelector(ctx *string, session *int) *StrictError {
	if session != nil {
		if *session < 0 || *session > 65535 {
			return &StrictError{
				Reason:  ReasonOutOfRange,
				Field:   "session",
				Message: fmt.Sprintf("field \"session\" must be in 0..65535, got %d", *session),
			}
		}
	}
	if ctx != nil && session != nil {
		want := fmt.Sprintf("wts:%d", *session)
		if strings.TrimSpace(*ctx) != want {
			return &StrictError{
				Reason:  ReasonSelectorConflict,
				Field:   "context",
				Message: fmt.Sprintf("\"context\" (%q) and \"session\" (%d) select different contexts", *ctx, *session),
			}
		}
	}
	return nil
}
