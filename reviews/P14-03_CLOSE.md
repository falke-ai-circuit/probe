# P14-03 — close

**Unit:** P14-03
**Workflow:** wf_probe1
**Commit:** `fd7d548` — `P14-03: §10 capture — X11 GetImage backend on the display seam`
**Review:** reviewed_by: `PENDING` (no independent verdict — review leg owed) · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `fd7d5483b480912b82d20745a1208e03924cacbd`
**Parent verification (orchestrator, this close):** `GOTOOLCHAIN=local go test ./... -count=1` re-run at `fd7d548` → **736 pass / 0 fail / 2 skip**, matching the unit (baseline 727, delta +9). Read in the tree: `hashCapturePixels` hashes the RGB8 buffer (`internal/agent/capture.go:366-371`) and `encodeCaptureFrame` is called after it (`:318`), so the encoded bytes cannot influence the hash; `Data`/`SizeBytes` carry `omitempty` (`internal/protocol/messages.go:563-564`), which is what makes C-8 key-absence structural rather than a convention; the decode guard writes `stage:"decode"` (`capture.go:417,432`) and the cost guard uses `protocol.CaptureMaxPixels` (`:406`); 6 `TestP1403*` cases live in `p1403_capture_test.go`. The unit also ran the falsification I asked for — a file-hashing implementation red-lines acceptance 1 — which is the only evidence that test is non-vacuous.
**Scope:** agent capture backend + §10 result types + capture-route error mapping + x11 feature row
**Artefact:** `/opt/data/tmp/probe119/P14-03_CAPTURE.md`

## Done

- `POST /api/v1/agents/{id}/capture` answers from a real X11 backend (`GetImage` on the
  `x11Conn` of P13-03/P14-01/02): full screen, window, region. `data`/`size_bytes` absent
  under `hash_only:true` (C-8); hash over decoded RGBA8 pixels (C-8); `width`/`height` are the
  captured pixels and a bad parse is 500 `INTERNAL_ERROR` `stage:"decode"` (SS-11).
- `changed`/`baseline`/`since` per §10 with an in-agent LRU-64 / TTL-600 s baseline cache keyed
  on `(context,target,dpi_scale)`; `REGION_TOO_LARGE` at >8 294 400 px before any capture, no
  downscale; `source` distinguishes `window_direct` from `screen_region`.
- Wayland selector → `403 PRIMITIVE_REFUSED reason:"CONSENT_REQUIRED"`.
- The x11 feature row no longer refuses `capture.window`/`capture.region` with `NO_BACKEND`.

## Evidence

| gate | result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./internal/agent ./internal/server ./internal/protocol` | exit 0 |
| `go test ./... -v -count=1` | 736 pass / 0 fail / 2 skip (baseline 727/0/2), exit 0 — `P14-03_suite.log` |
| real Xvfb pixel read-back | pixel (5,5) `#3366cc` on a 120x90 window at (10,20); full screen = server `Pixels()` |
| falsification of acceptance 1 | encoded-file hashing implementation fails `TestP1403PixelHashSurvivesReEncoding` (`6d2186b2…` vs `fb64a5f3…`), reverted |

## Open item

Redirected-window capture via `XCompositeNameWindowPixmap` → `source:"compositor"` is not
implemented: a redirected/unmapped window is a typed failure, never mislabelled `window_direct`.
