package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// T1 / A4: fs-write returns no digest. The manifest is the artefact the commit
// script parses, so a write that lands differently from the bytes handed in
// must be caught by the read-back hash before anything is armed or executed.
func TestStagePayloadRefusesCorruptManifestWrite(t *testing.T) {
	fake := newFake()
	fake.writeHook = func(_ int, req WriteRequest) ([]byte, error) {
		if strings.HasSuffix(req.Path, ManifestFile) {
			bad := append([]byte(nil), req.Data...)
			bad[0] ^= 0xFF
			return bad, nil
		}
		return nil, nil
	}

	_, err := NewRunner(fake).StagePayload(context.Background(), stageRequest([]byte("payload")))
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("corrupt manifest write was accepted (err = %v)", err)
	}
	if se.Step != "SW-05" || se.Code != CodeDigestMismatch {
		t.Fatalf("error = %s/%s, want SW-05/%s", se.Step, se.Code, CodeDigestMismatch)
	}
}

// T1 / A4: Written is the length the agent was handed, not proof of the bytes
// on disk. A short-write report on the manifest must be refused on the report
// alone, even though the digest would still match.
func TestStagePayloadRefusesShortManifestWrite(t *testing.T) {
	fake := newFake()
	fake.shortWritePath = ManifestFile

	_, err := NewRunner(fake).StagePayload(context.Background(), stageRequest([]byte("payload")))
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("short-write report was accepted (err = %v)", err)
	}
	if se.Step != "SW-05" || se.Code != CodeWriteFailed {
		t.Fatalf("error = %s/%s, want SW-05/%s", se.Step, se.Code, CodeWriteFailed)
	}
}

// T1: every write the sequence performs is proven by fs-hash, and the digest
// recorded as V4 is the digest of the bytes that were proven to land.
func TestStagePayloadWriteDigestsAreVerified(t *testing.T) {
	payload := []byte("payload-bytes")
	fake := newFake()
	res, err := NewRunner(fake).StagePayload(context.Background(), stageRequest(payload))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := SHA256Hex(fake.files[res.BootPath]); got != res.BootSHA256 {
		t.Fatalf("boot digest %s does not describe the landed script %s", res.BootSHA256, got)
	}
	if got := SHA256Hex(fake.files[res.ManifestPath]); got != res.ManifestSHA256 {
		t.Fatalf("V4 digest %s does not describe the landed manifest %s", res.ManifestSHA256, got)
	}
}
