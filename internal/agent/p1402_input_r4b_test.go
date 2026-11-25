package agent

// P14B-05 / R4b: an invalid UTF-8 BYTE is a repair too, and it is named in a
// second position space.
//
// R4 (P14B-02) labelled an unpaired CODE UNIT: `\ud800` becomes U+FFFD during
// decode, so the evidence lives only in the raw params, and the label reports
// 1-based code-unit positions. R4b is the other half of the same class: a byte
// that is not valid UTF-8 at all is replaced by U+FFFD the same way, with no
// error, and a byte has no code-unit position to name. So it gets its own key
// and its own offsets — 0-based BYTE offsets into the caller's raw `text`
// value — and the two spaces are asserted never to be confused.
//
// The tests live beside the R4 ones on purpose: the discrimination they protect
// is BETWEEN the shapes (a caller-sent U+FFFD, a lone surrogate, an invalid
// byte), and a reader has to see the three side by side to check it.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// p14b05RawBody builds a body whose `text` value carries `inner` VERBATIM as
// the bytes between the quotes, so an invalid byte reaches the decoder exactly
// as a hostile or broken client would send it.
func p14b05RawBody(inner string) string {
	return `{"context":"x11::0","window":"0x3c00007","text":"` + inner + `"}`
}

func TestP14B05InvalidUTF8IsLabelled(t *testing.T) {
	body := p14b05RawBody("a\xffb")

	// Premise, asserted so the label cannot be mistaken for a claim about the
	// string the agent holds: by then the byte IS U+FFFD, with no error.
	var params protocol.InputParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatalf("encoding/json refused the body: %v", err)
	}
	if params.Text != "a\ufffdb" {
		t.Fatalf("premise broken: decoded text = %q, want the byte replaced by U+FFFD", params.Text)
	}

	det := p1402Detail(t, p1402Agent(newFakeInput()).handleType(p1402RawEnvelope("tb", body)))
	if det["success"] != nil {
		_ = det["success"]
	}
	got, ok := det["invalid_utf8_bytes"]
	if !ok {
		t.Fatalf("an invalid UTF-8 byte was repaired in silence: detail = %v", det)
	}
	if got != float64(1) {
		t.Fatalf("invalid_utf8_bytes = %v, want 1", got)
	}
	at, ok := det["invalid_utf8_at"].([]interface{})
	if !ok || len(at) != 1 || at[0] != float64(1) {
		t.Fatalf("invalid_utf8_at = %v, want [1] (the byte after the literal's first byte)", det["invalid_utf8_at"])
	}
	// The byte space is not the unit space: R4's key must stay out of this
	// body entirely, and this body's offsets must never land in that array.
	if v, ok := det["repaired_rune_count"]; ok {
		t.Fatalf("an invalid byte was labelled as an unpaired code unit (repaired_rune_count=%v): detail = %v", v, det)
	}

	// The offset names a POSITION IN THE CALLER'S BYTES, stated as an
	// executable invariant: offset o addresses literal byte o+1 (the opening
	// quote is literal index 0).
	lit, ok := rawStringField(json.RawMessage(body), "text")
	if !ok {
		t.Fatal("rawStringField could not read text: the test cannot check the offset space")
	}
	off := int(at[0].(float64))
	if lit[off+1] != 0xff {
		t.Fatalf("invalid_utf8_at[0]=%d addresses literal byte %#x, want 0xff: the offset is not in the byte space", off, lit[off+1])
	}
}

func TestP14B05InvalidSequenceCountMatchesDecoder(t *testing.T) {
	// A truncated three-byte sequence: two bytes the decoder cannot carry, and
	// it writes exactly two U+FFFD — so the count is the decoder's own
	// substitution count, not a guess about "one broken character".
	body := p14b05RawBody("a\xe2\x82b")

	var params protocol.InputParams
	if err := json.Unmarshal([]byte(body), &params); err != nil {
		t.Fatalf("encoding/json refused the body: %v", err)
	}
	if n := strings.Count(params.Text, "\ufffd"); n != 2 {
		t.Fatalf("premise broken: decoder produced %d U+FFFD in %q, want 2", n, params.Text)
	}

	det := p1402Detail(t, p1402Agent(newFakeInput()).handleType(p1402RawEnvelope("tc", body)))
	if det["invalid_utf8_bytes"] != float64(2) {
		t.Fatalf("invalid_utf8_bytes = %v, want 2 (one per byte the decoder replaced): detail = %v", det["invalid_utf8_bytes"], det)
	}
	at, ok := det["invalid_utf8_at"].([]interface{})
	if !ok || len(at) != 2 || at[0] != float64(1) || at[1] != float64(2) {
		t.Fatalf("invalid_utf8_at = %v, want [1 2]", det["invalid_utf8_at"])
	}
}

func TestP14B05ByteAndUnitSpacesDoNotCollide(t *testing.T) {
	// One body, both faults: an invalid byte at text byte 1, then a lone
	// surrogate whose repaired code unit is the 3rd in the decoded string.
	// Both must be reported, each in its own space — a single key carrying
	// both arrays would make [1] and [3] look like comparable positions.
	body := p14b05RawBody("a\xff\\ud800b")

	det := p1402Detail(t, p1402Agent(newFakeInput()).handleType(p1402RawEnvelope("td", body)))

	if det["invalid_utf8_bytes"] != float64(1) {
		t.Fatalf("invalid_utf8_bytes = %v, want 1: detail = %v", det["invalid_utf8_bytes"], det)
	}
	bat, ok := det["invalid_utf8_at"].([]interface{})
	if !ok || len(bat) != 1 || bat[0] != float64(1) {
		t.Fatalf("invalid_utf8_at = %v, want [1]", det["invalid_utf8_at"])
	}
	if det["repaired_rune_count"] != float64(1) {
		t.Fatalf("repaired_rune_count = %v, want 1: the surrogate after the bad byte was lost", det["repaired_rune_count"])
	}
	rat, ok := det["repaired_at"].([]interface{})
	if !ok || len(rat) != 1 || rat[0] != float64(3) {
		t.Fatalf("repaired_at = %v, want [3] (1-based code units: a, U+FFFD, then the surrogate)", det["repaired_at"])
	}
}

func TestP14B05ThreeShapesThreeOutcomes(t *testing.T) {
	// The acceptance is discrimination, not the presence of a field: three
	// bodies that are byte-different, two of which decode to the SAME string,
	// must produce three different observable outcomes.
	shapes := []struct {
		name string
		body string
		want string
	}{
		{"caller_sent_fffd", p14b05RawBody("a\\ufffdb"), ""},
		{"lone_surrogate", p14b05RawBody("a\\ud800b"), "rune"},
		{"invalid_bytes", p14b05RawBody("a\xffb"), "byte"},
	}

	signatures := map[string]string{}
	for _, sh := range shapes {
		det := p1402Detail(t, p1402Agent(newFakeInput()).handleType(p1402RawEnvelope("ts-"+sh.name, sh.body)))
		sig := outcomeSignature(det)
		signatures[sh.name] = sig
		if sig != sh.want {
			t.Fatalf("%s: outcome = %q, want %q (detail = %v)", sh.name, sig, sh.want, det)
		}
	}
	seen := map[string]string{}
	for _, sh := range shapes {
		if other, dup := seen[signatures[sh.name]]; dup {
			t.Fatalf("%s and %s share the outcome %q: the label does not discriminate",
				other, sh.name, signatures[sh.name])
		}
		seen[signatures[sh.name]] = sh.name
	}
}

// outcomeSignature names which repair label a detail carries, as a value a test
// can compare: "" for none, "rune" for R4's pair, "byte" for R4b's. It fails
// loudly on a detail that carries both, because that is a body the three-shape
// table does not construct.
func outcomeSignature(det map[string]interface{}) string {
	_, runeCount := det["repaired_rune_count"]
	_, runeAt := det["repaired_at"]
	_, byteCount := det["invalid_utf8_bytes"]
	_, byteAt := det["invalid_utf8_at"]
	if runeCount != runeAt || byteCount != byteAt {
		return "half-a-pair"
	}
	switch {
	case runeCount && byteCount:
		return "both"
	case runeCount:
		return "rune"
	case byteCount:
		return "byte"
	}
	return ""
}
