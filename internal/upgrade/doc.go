// Package upgrade implements the out-of-band PROBE client upgrade path: the
// bootstrap artefacts (PowerShell scripts, manifest, journal) and the caller
// side sequence SW-00..SW-08 that stages and arms them through a live agent.
//
// Design contract:
//
//	P2-01_BOOTSTRAP_DESIGN.md  (D1-D5, sequence SW-n, failure table F1-F13)
//	P2-02_SWAP_DESIGN.md       (S1-S5, unified sequence SW-00..SW-20)
//
// Only primitives that exist in the deployed client are used: exec, fs-write,
// fs-hash, fs-stat, fs-read. Nothing here requires the new binary to be alive.
package upgrade

import "fmt"

// StepError is a sequence failure carrying the SW step it happened in and a
// machine-readable code that matches the journal vocabulary.
type StepError struct {
	Step string // SW id, e.g. "SW-02"
	Code string // e.g. "SANDBOX_REFUSED"
	Err  error
}

func (e *StepError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Step, e.Code)
	}
	return fmt.Sprintf("%s: %s: %v", e.Step, e.Code, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

func stepErr(step, code string, err error) *StepError {
	return &StepError{Step: step, Code: code, Err: err}
}
