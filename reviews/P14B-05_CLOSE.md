# P14B-05 — close record (R4b: an invalid UTF-8 byte is reported in the byte space)

**Unit:** P14B-05 (workflow wf_probe1) · branch `r119-build`
**Commit:** `734c66e6007916686eeedb909a95e051adadc5ef` — code, tests and the label keys. The artefact and this
record follow in a document-only commit on top of it, so the reviewed HEAD stays `734c66e`.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 734c66e`
**Scope:** `internal/agent/input_pointer.go`, new `internal/agent/p1402_input_r4b_test.go`,
`internal/protocol/input_geometry.go`. `internal/server/api_v1_transfer.go` untouched; contract untouched;
no live-desktop claim. Artefact: `/opt/data/tmp/probe119/P14B-05_R4b.md` (committed as `reviews/P14B-05_R4b.md`).

## What changed

`jsonStringScan` replaced `jsonStringUnits` and returns **both** evidence products from one pass: the UTF-16 code
units R4 already used, plus the byte offsets of every byte that is not valid UTF-8. An invalid byte is
substituted by one U+FFFD unit and the walk continues, matching `encoding/json`'s own rule — one replacement per
invalid byte — so unit positions stay aligned with the string the agent received and a lone surrogate after a bad
byte is no longer swallowed.

`textDetail` now fills two independent pairs on `protocol.TextDetail`: `repaired_rune_count`/`repaired_at`
(1-based code units, R4) and `invalid_utf8_bytes`/`invalid_utf8_at` (0-based byte offsets into the raw `text`
literal, R4b). Each is omitted when its own kind was not repaired.

## Proof

- **Gap reproduced before the fix:** `"a\xffb"` decodes with `err=<nil>` to `"a\uFFFDb"`, `jsonStringUnits`
  returns `ok=false`, `textRepairs` is empty, and the response is
  `{"success":true,"detail":{"transcoded":false,"encoding":"utf8","codepoints":3}}` — repair with no label.
  Quoted with request bytes in artefact §1, agent-side and through `POST /api/v1/agents/{id}/text-input`.
- **Byte path is not lost upstream:** the server forwards `61 ff 62` verbatim; the repair happens in the decoder
  inside the agent. No server file touched.
- **Red before:** 4/4 new tests FAIL at `fdd5188` with "repaired in silence" / `invalid_utf8_bytes = <nil>`.
  The mixed body additionally shows the pre-fix loss of R4 evidence after a bad byte.
- **Green after:** 4/4 PASS at `734c66e`, quoted in artefact §4.
- **Discrimination:** three bodies (caller-sent U+FFFD, lone surrogate, invalid byte) produce three pairwise
  different signatures, computed from key presence; the mixed body reports `invalid_utf8_at:[1]` and
  `repaired_at:[3]` in one response, proving the two spaces are carried separately.
- **Count is the decoder's, not a guess:** truncated `\xe2\x82` → `invalid_utf8_bytes:2`, equal to the number of
  U+FFFD the decoder produced in the same run.

## Suite

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` → exit 0, **763 pass / 0 fail / 2 skip**
(633 top-level + 130 subtests); baseline `fdd5188` re-measured here 759/0/2. +4 tests, none removed, no assertion
weakened. `go build ./...` and `go vet ./internal/...` clean.

## Open item

`invalid_utf8_at` addresses the raw `text` literal, so a body with escapes before the bad byte reports an offset
that is not the byte's index in the decoded text. The offset still names the offending byte exactly; a decoded-byte
position space would need a second decoder, which R4's design deliberately avoided. Named, not absorbed.
