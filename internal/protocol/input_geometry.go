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
)

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
