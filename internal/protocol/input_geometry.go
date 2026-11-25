package protocol

// P14-01 (contract P10-07 v3.1 §9.1): the geometry rule's answer shape.
//
// Two outcomes live here, and they are different in kind:
//
//   - a REFUSAL (404/403/501): nothing ran, so the body carries an error code
//     and `ran:false` — the §11 shape.
//   - an OBSERVED FAILURE (HTTP 200, ok:false): the request ran, the platform's
//     observed state did not permit the input, so the body carries the §8.1
//     closed `reason`, the covering window, and `ran:false`.
//
// Mixing the two is what made C-7 uncatchable, so the observed shape has its own
// type and its own enum (`ObservedReasons`).

import (
	"encoding/json"
	"fmt"
)

const (
	// ErrWindowNotFound is §9.1's pre-injection refusal for a `window` that is
	// not in the resolved context's tree. 404, and nothing was injected.
	ErrWindowNotFound = "WINDOW_NOT_FOUND"

	// §8.1 observed-failure reasons (closed, HTTP 200 + ok:false).
	ReasonOccluded         = "OCCLUDED"
	ReasonForegroundDenied = "FOREGROUND_DENIED"
	ReasonNotSupported     = "NOT_SUPPORTED"

	// §11 403 reason: the primitive needs a focused window and was not given
	// permission to take focus (`focus:true`).
	ReasonNoFocusTarget = "NO_FOCUS_TARGET"

	// §9.3: `unit:"px"` is refused on every backend that lacks a pixel-precise
	// wheel. A partial scroll moves the list by an unknown amount, which is
	// worse than a refusal.
	ReasonUnitNotSupported = "UNIT_NOT_SUPPORTED"

	// §9.4: a codepoint the backend's keycode range cannot carry. The refusal
	// NAMES the codepoint; the string is never typed as a prefix.
	ReasonCodepointUnsupported = "CODEPOINT_UNSUPPORTED"
)

// ClickResult is §9.2's answer: the count that was emitted and the gap the
// backend MEASURED between the two presses, not a constant it was handed.
// `gap_ms` is present whenever count>1, because that is the number the app's
// double-click rule is judged against.
type ClickResult struct {
	Success bool `json:"success"`
	Count   int  `json:"count"`
	GapMS   int  `json:"gap_ms,omitempty"`
}

// ScrollResult is §9.3's answer. `notches` is how many wheel events the backend
// emitted: |delta| on every backend this build has, and the number is echoed so
// a caller can see that a delta of 3 was not collapsed to one click.
type ScrollResult struct {
	Success bool   `json:"success"`
	Delta   int    `json:"delta"`
	Unit    string `json:"unit"`
	Notches int    `json:"notches"`
}

// TextDetail is §9.4's `detail`: `transcoded` is true when the caller's
// encoding was not the encoding the text was emitted in, so a caller that sent
// UTF-16 units knows probe did not pass them through verbatim.
type TextDetail struct {
	Transcoded  bool   `json:"transcoded"`
	Encoding    string `json:"encoding,omitempty"`
	CodePoints  int    `json:"codepoints"`
}

// TextResult is §9.4's success shape.
type TextResult struct {
	Success bool        `json:"success"`
	Detail  *TextDetail `json:"detail,omitempty"`
}

// ObservedReasons is §8.1's closed enum, verbatim. Every value has a producer in
// the input/window path; a reason not listed here is a defect, not an extension.
var ObservedReasons = []string{ReasonOccluded, ReasonForegroundDenied, ReasonNotSupported}

// OcclusionHit names the window that is actually on top at the computed point.
// `owner_pid` is omitted when the platform did not publish it — an unknown
// owner is not pid 0.
type OcclusionHit struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	OwnerPID int    `json:"owner_pid,omitempty"`
}

// InputFailureData is the §9.1 `data` object:
//
//	{"reason":"OCCLUDED","hit":{...},"detail":{"intended":…,"point":{…}},"ran":false}
type InputFailureData struct {
	Reason string                 `json:"reason"`
	Hit    *OcclusionHit          `json:"hit,omitempty"`
	Detail map[string]interface{} `json:"detail"`
	Ran    bool                   `json:"ran"`
}

// InputFailure is the HTTP-200 `ok:false` body. `context_ref` sits at the top
// level for the same reason UITreeResult carries it: every response names the
// context it acted in, so a caller cannot attribute an OCCLUDED answer to the
// wrong display.
type InputFailure struct {
	OK         bool             `json:"ok"`
	ContextRef UITreeContextRef `json:"context_ref"`
	Data       InputFailureData `json:"data"`
}

// NewInputFailure builds the observed-failure envelope. It is a RESULT
// envelope, not an error: the HTTP status is 200 precisely because the request
// ran and the platform said no.
func NewInputFailure(id, resultType string, ref UITreeContextRef, reason string, hit *OcclusionHit, detail map[string]interface{}) Envelope {
	if detail == nil {
		detail = map[string]interface{}{}
	}
	if _, has := detail["ran"]; !has {
		detail["ran"] = false
	}
	body, err := json.Marshal(InputFailure{
		OK:         false,
		ContextRef: ref,
		Data: InputFailureData{
			Reason: reason,
			Hit:    hit,
			Detail: detail,
			Ran:    false,
		},
	})
	if err != nil {
		return NewError(id, ErrInternal, fmt.Sprintf("failed to marshal input failure: %v", err))
	}
	return Envelope{ID: id, Type: resultType, Result: body}
}
