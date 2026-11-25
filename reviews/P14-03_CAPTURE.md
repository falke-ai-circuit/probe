# P14-03 — contract §10 capture (X11, headless Xvfb)

**Unit:** P14-03
**Workflow:** wf_probe1
**Commit:** `fd7d548` (branch `r119-build`, pushed; baseline `8cdbb18`, parent docs head `ee579bd`)
**Review:** reviewed_by: PENDING — review leg owed
**Scope:** `internal/agent/capture.go`, `internal/agent/capture_linux.go`,
`internal/protocol/messages.go` (§10 result types), `internal/agent/x11.go` (pixmap formats,
resource-id range), `internal/agent/contexts.go` (x11 feature row), `internal/server/api_v1.go`
path for the capture route (`server_api.go`, `strict.go`), `internal/agent/p1403_capture_test.go`.
Not touched: §9 input paths, the contract document, any other route.

## What replaced NO_BACKEND

| old | new |
|---|---|
| `agent.handleCapture` → `plat.CaptureDisplay` → `import`/`scrot` shell-out, `Width:0, Height:0` | `agent.handleCapture` (capture.go) → `captureSeam` implemented by `*x11Conn` (capture_linux.go) |
| `x11Features`: `capture.region` gated on `import`/`scrot`, `capture.window` refused `NO_BACKEND` | both advertised `true` — `XGetImage` needs no shell tool |
| capture route returned the agent's error body as a 200 `data` object | `writeAgentRefusal` on the `/capture` route; `INTERNAL_ERROR` → 500 |

Mechanism: `XGetImage` (opcode 73, ZPixmap, AllPlanes) on the **same** `x11Conn` P13-03 opened for
the tree and P14-01/02 used for geometry/input — one connection, one view of the server.
Targets: full screen `XGetImage(root)`, window `XGetImage(window)`, region `XGetImage(rect)`.
A rect **with** a window crops inside that window's drawable (still `window_direct`).

Decoding: the reply's own `reply-length` is the stride authority (`stride = replyLen / h`), the
setup-reply pixmap-format block supplies bits-per-pixel, only TrueColor 32bpp (BGRA→RGBA) and
16bpp (RGB565) are decoded. Anything else is `captureDecodeError`.

## Field semantics as implemented

| field | implementation |
|---|---|
| `hash` | `sha256:` + hex over the RGBA8 buffer, stride normalised to `w*4` in the backend; `encodeCaptureFrame` runs **after** hashing and cannot influence it |
| `data`,`size_bytes` | `omitempty` on `CaptureV1Result` — absent under `hash_only:true`, never `""`/`null` |
| `width`,`height` | the frame the backend validated; `W<=0 || H<=0 || len(Pix)!=W*H*4` → 500 `INTERNAL_ERROR` `detail{backend:"x11",stage:"decode"}` |
| `changed` | `*bool`, JSON `null` when no baseline; `true`/`false` vs the baseline otherwise |
| `baseline` | the compared hash, `""` when none |
| `since` | overrides the baseline for that call only; the call does **not** put its hash in the cache |
| cache | key `(resolved context, kind:target, 1.0)`; LRU 64, TTL 600 s, in-agent (`captureBaselines`), never persisted |
| `source` | `window_direct` (window drawable) / `screen_region` (screen or rect) / `compositor` reserved |
| `detail.encoded_by` | `"agent"` when data is present (C-9: the backend hands over raw pixels, the agent encodes both formats) |
| cost guard | `w*h > 8_294_400` → 400 `INVALID_PARAMS` `reason:"REGION_TOO_LARGE"` `detail.max_px:8294400`; refused before capture, no downscale |

## Acceptance — test and result

| # | item | test | result |
|---|---|---|---|
| 1 | hash of pixels, not file | `TestP1403PixelHashSurvivesReEncoding` — png and jpeg of identical pixels must hash equal and equal the sha256 of the raw RGBA; a mutated byte must differ; the two encodings are asserted to be different base64, so the equality is about the hash | PASS |
| 2 | `hash_only:true` key absence | `TestP1403HashOnlyOmitsKeys` — asserts the **keys** `data`,`size_bytes` are absent from the decoded JSON; present as non-empty base64 + integer size when false, png decodes, `size_bytes == len(decoded)` | PASS |
| 3 | no 0-sized success | `TestP1403ZeroSizedCaptureIsAnError` — `0x0`, short pixel buffer, nil frame → `INTERNAL_ERROR`, `detail.stage=="decode"`, `detail.backend` non-empty | PASS (3 subtests) |
| 4 | `changed`/`baseline`/`since` | `TestP1403ChangedBaselineAndSince` — first `null`+`""`; identical → `false` with previous hash; mutated pixel → `true`; `since` overrides and the stored baseline survives the since call | PASS |
| 5 | `REGION_TOO_LARGE` boundary | `TestP1403RegionTooLargeBoundary` — 3840x2160 (8 294 400) accepted, backend called once; 3840x2161 → 400 `REGION_TOO_LARGE` `max_px`, backend **not** called, no `data` key | PASS |
| 6 | real Xvfb pixels | `TestP1403RealXvfbPixelsAndSourceLabel` — `Xvfb :N 320x240x24 -ac`, window 120x90 at (10,20) with pixel `0x3366CC` created by `CreateWindow` (override-redirect, no WM), captured; `png` decoded, pixel (5,5) `#3366cc`, `width/height` 120x90 | PASS |
| 7 | `source` honesty | same test — rect equal to that window's rect reports `screen_region` (and its pixels still read back `#3366cc`); window capture reports `window_direct`; full screen reports the server's own `Pixels()` and `screen_region` | PASS |

### Acceptance 1's required falsification

The test was run against a deliberately wrong implementation (handler hashing the encoded file):

```
hash = "sha256:6d2186b2…" want sha256 of the decoded pixels "sha256:fb64a5f3…"
--- FAIL: TestP1403PixelHashSurvivesReEncoding (0.01s)
```

Reverted; the committed tree passes. An implementation that hashes encoded bytes cannot pass
this test, because png and jpeg of the same pixels are different files.

## Suite

`GOTOOLCHAIN=local go test ./... -v -count=1`, log `/opt/data/tmp/probe119/P14-03_suite.log`:

```
--- PASS : 736   --- FAIL : 0   --- SKIP : 2
ok  internal/agent 1.846s   ok  internal/server 10.518s   (all packages ok, exit 0)
```

Baseline `8cdbb18`: 727 pass / 0 fail / 2 skip. Measured now: 736 pass — delta +9
(`TestP1403*`: 5 top-level cases, `TestP1403ZeroSizedCaptureIsAnError` carrying 3 subtests).
Skips unchanged: `TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer`.

## Open item

`XCompositeNameWindowPixmap` (redirected window → `source:"compositor"`) is **not implemented**.
Plain `XGetImage(window)` is the shipped path; a redirected/unmapped window surfaces as a typed
`INTERNAL_ERROR` with `detail{stage:"capture"}` — reported, never relabelled as `window_direct`.
Wayland answers `403 PRIMITIVE_REFUSED reason:"CONSENT_REQUIRED"`; Termux/a11y rows are not
reachable from this linux build, so they carry no path here.
