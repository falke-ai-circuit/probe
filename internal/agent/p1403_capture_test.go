//go:build linux

package agent

// P14-03 (contract P10-07 v3.1 §10) — the seven acceptance items.
//
// The three field traps get tests that FAIL on the plausible-but-wrong
// implementation, which is the point of writing them:
//   - an implementation that hashes the encoded file passes every test except
//     TestP1403PixelHashSurvivesReEncoding, where png and jpeg of the same
//     pixels must agree;
//   - an implementation that emits data:"" under hash_only passes an
//     "is it empty?" assertion and fails TestP1403HashOnlyOmitsKeys, which
//     asserts KEY ABSENCE;
//   - an implementation that reports 0x0 as success passes a happy-path test
//     and fails TestP1403ZeroSizedCaptureIsAnError.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// --- seam doubles -----------------------------------------------------------

// fakeCapture is BOTH the windowProbe the display seam hands back and the
// captureSeam the handler consumes, so a test drives §10 without an X server.
type fakeCapture struct {
	plans []capturePlan
	frame *captureFrame
	err   error
}

func (f *fakeCapture) Capture(p capturePlan) (*captureFrame, error) {
	f.plans = append(f.plans, p)
	return f.frame, f.err
}
func (f *fakeCapture) Close() error { return nil }

func (f *fakeCapture) Root() uint32                              { return 1 }
func (f *fakeCapture) Pixels() (int, int)                        { return 640, 480 }
func (f *fakeCapture) Property(uint32, string) (propValue, error) { return propValue{}, errPropAbsent }
func (f *fakeCapture) Attributes(uint32) (x11Attrs, error) {
	return x11Attrs{MapState: x11MapStateViewable}, nil
}
func (f *fakeCapture) Geometry(uint32) (x11Geometry, error) { return x11Geometry{W: 4, H: 3}, nil }
func (f *fakeCapture) TranslateToRoot(uint32) (int, int, error) { return 0, 0, nil }
func (f *fakeCapture) AtomName(uint32) (string, error)         { return "", nil }
func (f *fakeCapture) PointIn(uint32, int, int) (int32, int32, uint32, error) {
	return 0, 0, 0, nil
}
func (f *fakeCapture) RequestForeground(uint32) error { return nil }

// testPattern is a deterministic 4x3 RGBA8 frame: distinct pixels so a
// single-pixel mutation is observable.
func testPattern() *captureFrame {
	pix := make([]byte, 4*3*4)
	for i := 0; i < 12; i++ {
		pix[i*4+0] = byte(10 + i)
		pix[i*4+1] = byte(20 + i)
		pix[i*4+2] = byte(30 + i)
		pix[i*4+3] = 255
	}
	return &captureFrame{Pix: pix, W: 4, H: 3, Source: protocol.CaptureSourceWindowDirect}
}

func captureAgent(f *fakeCapture) *Agent {
	a := &Agent{}
	a.openDisplay = func(num int) (windowProbe, error) { return f, nil }
	captureBaselines.reset()
	return a
}

func captureEnvelope(t *testing.T, body string) protocol.Envelope {
	t.Helper()
	return protocol.Envelope{ID: "t-capture", Type: protocol.TypeCapture, Params: json.RawMessage(body)}
}

func runCapture(t *testing.T, a *Agent, body string) protocol.Envelope {
	t.Helper()
	return a.handleCapture(captureEnvelope(t, body))
}

func captureDataMap(t *testing.T, env protocol.Envelope) map[string]interface{} {
	t.Helper()
	if env.Error != nil {
		t.Fatalf("capture refused: %s %s %v", env.Error.Code, env.Error.Message, env.Error.Detail)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(env.Result, &m); err != nil {
		t.Fatalf("result is not a JSON object: %v", err)
	}
	return m
}

func captureResultOf(t *testing.T, env protocol.Envelope) protocol.CaptureV1Result {
	t.Helper()
	var out protocol.CaptureV1Result
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("result does not decode into CaptureV1Result: %v", err)
	}
	return out
}

// --- 1. pixel hash, not file hash ------------------------------------------

func TestP1403PixelHashSurvivesReEncoding(t *testing.T) {
	base := testPattern()
	want := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256(base.Pix); return s[:] }())

	sep := captureAgent(&fakeCapture{frame: testPattern()})
	pngRes := captureResultOf(t, runCapture(t, sep, `{"context":"x11::0","format":"png","hash_only":true}`))
	jpegRes := captureResultOf(t, runCapture(t, sep, `{"context":"x11::0","format":"jpeg","hash_only":true}`))

	if pngRes.Hash != want {
		t.Fatalf("hash = %q, want sha256 of the decoded pixels %q", pngRes.Hash, want)
	}
	// This is the assertion an encoded-bytes implementation fails: png and
	// jpeg of identical pixels are different files, so their file hashes
	// differ, while the decoded-pixel hash cannot.
	if pngRes.Hash != jpegRes.Hash {
		t.Fatalf("same pixels hashed differently by format: png %q jpeg %q — the hash is covering the encoded file, not the pixels", pngRes.Hash, jpegRes.Hash)
	}

	// ...and prove the two encodings really are different bytes, so the equality
	// above is a statement about the hash and not about the encoder.
	pf := captureResultOf(t, runCapture(t, sep, `{"context":"x11::0","format":"png","hash_only":false}`))
	jf := captureResultOf(t, runCapture(t, sep, `{"context":"x11::0","format":"jpeg","hash_only":false}`))
	if pf.Data == jf.Data {
		t.Fatal("png and jpeg produced identical base64: the format field is not reaching the encoder, so the hash comparison proves nothing")
	}
	if pf.Hash != jf.Hash || pf.Hash != want {
		t.Fatalf("hash moved when only the encoding changed: png %q jpeg %q", pf.Hash, jf.Hash)
	}

	// One pixel changed, everything else identical.
	mutated := testPattern()
	mutated.Pix[5] ^= 0x08
	mutAgent := captureAgent(&fakeCapture{frame: mutated})
	mut := captureResultOf(t, runCapture(t, mutAgent, `{"context":"x11::0","format":"png","hash_only":true}`))
	if mut.Hash == want {
		t.Fatal("a single changed pixel did not change the hash: the hash is not covering every pixel")
	}
}

// --- 2. hash_only key absence (C-8) ----------------------------------------

func TestP1403HashOnlyOmitsKeys(t *testing.T) {
	a := captureAgent(&fakeCapture{frame: testPattern()})

	hashOnly := captureDataMap(t, runCapture(t, a, `{"context":"x11::0","format":"png","hash_only":true}`))
	for _, key := range []string{"data", "size_bytes"} {
		if _, present := hashOnly[key]; present {
			t.Fatalf("hash_only:true response carries %q (=%v); C-8 says the KEY is absent, not empty and not null",
				key, hashOnly[key])
		}
	}

	full := captureDataMap(t, runCapture(t, a, `{"context":"x11::0","format":"png","hash_only":false}`))
	raw, present := full["data"]
	if !present {
		t.Fatal("hash_only:false response has no data key")
	}
	b64, isStr := raw.(string)
	if !isStr || b64 == "" {
		t.Fatalf("data = %v, want a non-empty base64 string", raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(decoded) == 0 {
		t.Fatalf("data is not decodable base64: %v (len %d)", err, len(decoded))
	}
	if _, err := png.Decode(bytes.NewReader(decoded)); err != nil {
		t.Fatalf("data is not a png: %v", err)
	}
	size, present := full["size_bytes"]
	if !present {
		t.Fatal("hash_only:false response has no size_bytes key")
	}
	if got := int64(size.(float64)); got != int64(len(decoded)) {
		t.Fatalf("size_bytes = %d, want the encoded length %d", got, len(decoded))
	}
	if h, ok := full["hash"].(string); !ok || h == "" {
		t.Fatalf("hash = %v, want a non-empty string", full["hash"])
	}
}

// --- 3. no 0-sized success --------------------------------------------------

func TestP1403ZeroSizedCaptureIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		frame *captureFrame
	}{
		{"zero dims", &captureFrame{Pix: []byte{}, W: 0, H: 0}},
		{"short pixel buffer", &captureFrame{Pix: make([]byte, 10), W: 4, H: 3}},
		{"no frame at all", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := captureAgent(&fakeCapture{frame: tc.frame})
			env := runCapture(t, a, `{"context":"x11::0","format":"png","hash_only":true}`)
			if env.Error == nil {
				t.Fatalf("capture with %s answered success (%s); §10 says 0 is never a success", tc.name, env.Result)
			}
			if env.Error.Code != protocol.ErrInternal {
				t.Fatalf("code = %q, want %q", env.Error.Code, protocol.ErrInternal)
			}
			if got := env.Error.Detail["stage"]; got != "decode" {
				t.Fatalf("detail.stage = %v, want \"decode\"", got)
			}
			if got := env.Error.Detail["backend"]; got == "" || got == nil {
				t.Fatalf("detail.backend missing: %v", env.Error.Detail)
			}
		})
	}
}

// --- 4. changed / baseline / since -----------------------------------------

func TestP1403ChangedBaselineAndSince(t *testing.T) {
	a := captureAgent(&fakeCapture{frame: testPattern()})

	first := captureResultOf(t, runCapture(t, a, `{"context":"x11::0","format":"png","hash_only":true}`))
	if first.Changed != nil {
		t.Fatalf("first sight reported changed=%v; with no baseline it must be null", *first.Changed)
	}
	if first.Baseline != "" {
		t.Fatalf("first sight baseline = %q, want \"\"", first.Baseline)
	}

	second := captureResultOf(t, runCapture(t, a, `{"context":"x11::0","format":"png","hash_only":true}`))
	if second.Changed == nil || *second.Changed {
		t.Fatalf("identical second capture changed=%v, want false", second.Changed)
	}
	if second.Baseline != first.Hash {
		t.Fatalf("baseline = %q, want the previous hash %q", second.Baseline, first.Hash)
	}

	// A different pixel is a different target observation, same target key.
	moved := captureAgent(&fakeCapture{frame: testPattern()})
	runCapture(t, moved, `{"context":"x11::0","format":"png","hash_only":true}`)
	mutated := testPattern()
	mutated.Pix[0] = 0xff
	movedFrame := &fakeCapture{frame: mutated}
	moved.openDisplay = func(num int) (windowProbe, error) { return movedFrame, nil }
	third := captureResultOf(t, runCapture(t, moved, `{"context":"x11::0","format":"png","hash_only":true}`))
	if third.Changed == nil || !*third.Changed {
		t.Fatalf("a changed pixel reported changed=%v, want true (baseline %q, hash %q)", third.Changed, third.Baseline, third.Hash)
	}

	// `since` overrides the stored baseline for THIS call, and must not
	// overwrite what the cache holds.
	stored := third.Hash
	fakeSince := "sha256:" + fmt.Sprintf("%064x", 1)
	overridden := captureResultOf(t, runCapture(t, moved, `{"context":"x11::0","format":"png","hash_only":true,"since":"`+fakeSince+`"}`))
	if overridden.Baseline != fakeSince {
		t.Fatalf("since call baseline = %q, want the request's %q", overridden.Baseline, fakeSince)
	}
	if overridden.Changed == nil || !*overridden.Changed {
		t.Fatalf("since comparison reported changed=%v against a different hash", overridden.Changed)
	}
	after := captureResultOf(t, runCapture(t, moved, `{"context":"x11::0","format":"png","hash_only":true}`))
	if after.Baseline != stored {
		t.Fatalf("a since call overwrote the stored baseline: now %q, was %q", after.Baseline, stored)
	}
	if after.Changed == nil || *after.Changed {
		t.Fatalf("stored baseline was lost: changed=%v", after.Changed)
	}
}

// --- 5. REGION_TOO_LARGE boundary ------------------------------------------

func TestP1403RegionTooLargeBoundary(t *testing.T) {
	// 3840x2160 = 8_294_400 px exactly.
	const side = 3840
	exactH := protocol.CaptureMaxPixels / side // 2160

	allowedFake := &fakeCapture{frame: testPattern()}
	allowed := captureAgent(allowedFake)
	env := runCapture(t, allowed, fmt.Sprintf(`{"context":"x11::0","rect":{"x":0,"y":0,"w":%d,"h":%d},"format":"png","hash_only":true}`, side, exactH))
	if env.Error != nil {
		t.Fatalf("exactly %d px was refused: %s %v", protocol.CaptureMaxPixels, env.Error.Code, env.Error.Detail)
	}
	if len(allowedFake.plans) != 1 {
		t.Fatalf("backend called %d times for an allowed region, want 1", len(allowedFake.plans))
	}

	overFake := &fakeCapture{frame: testPattern()}
	over := captureAgent(overFake)
	env = runCapture(t, over, fmt.Sprintf(`{"context":"x11::0","rect":{"x":0,"y":0,"w":%d,"h":%d},"format":"png","hash_only":false}`, side, exactH+1))
	if env.Error == nil {
		t.Fatalf("one pixel over the limit was accepted: %s", env.Result)
	}
	if env.Error.Code != protocol.ErrInvalidParams {
		t.Fatalf("code = %q, want %q", env.Error.Code, protocol.ErrInvalidParams)
	}
	if got := env.Error.Detail["reason"]; got != protocol.ReasonRegionTooLarge {
		t.Fatalf("reason = %v, want %q", got, protocol.ReasonRegionTooLarge)
	}
	if got := anyInt(env.Error.Detail["max_px"]); got != protocol.CaptureMaxPixels {
		t.Fatalf("detail.max_px = %v, want %d", env.Error.Detail["max_px"], protocol.CaptureMaxPixels)
	}
	// No downscaled image: the backend never ran, and the refusal carries no
	// data to mistake for a capture.
	if len(overFake.plans) != 0 {
		t.Fatalf("backend ran for a too-large region (%d calls); the guard must refuse before capturing", len(overFake.plans))
	}
	m := map[string]interface{}{}
	_ = json.Unmarshal(env.Result, &m)
	if _, has := m["data"]; has {
		t.Fatal("refusal body carries data: a downscaled capture was produced")
	}
}

// --- 6 + 7. real Xvfb pixels, and source honesty ---------------------------

func TestP1403RealXvfbPixelsAndSourceLabel(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial Xvfb :%d: %v", num, err)
	}
	defer x.Close()

	const (
		winW, winH = 120, 90
		winX, winY = 10, 20
		bgPixel    = 0x3366CC
	)
	win := x11TestWindow(t, x, winX, winY, winW, winH, bgPixel)

	ctx := fmt.Sprintf("x11::%d", num)
	a := &Agent{}
	a.openDisplay = defaultDisplayOpener
	captureBaselines.reset()

	// Window capture: 500-class errors here would mean the drawn surface was
	// not readable at all, which is a real finding, not a skip.
	env := runCapture(t, a, fmt.Sprintf(`{"context":%q,"window":"0x%x","format":"png","hash_only":false}`, ctx, win))
	m := captureDataMap(t, env)
	if got := m["source"]; got != protocol.CaptureSourceWindowDirect {
		t.Fatalf("window capture source = %v, want %q", got, protocol.CaptureSourceWindowDirect)
	}
	if int(m["width"].(float64)) != winW || int(m["height"].(float64)) != winH {
		t.Fatalf("captured %vx%v, want the drawn surface %dx%d",
			m["width"], m["height"], winW, winH)
	}
	img := decodeShownImage(t, m)
	if r, g, b, _ := img.At(5, 5).RGBA(); r>>8 != 0x33 || g>>8 != 0x66 || b>>8 != 0xCC {
		t.Fatalf("pixel (5,5) = #%02x%02x%02x, want #3366cc: the pixels were not read from the server",
			r>>8, g>>8, b>>8)
	}

	// Region over the SAME window's rect lifts screen pixels: it must say
	// screen_region, never window_direct. This is the §10 honesty rule.
	env = runCapture(t, a, fmt.Sprintf(`{"context":%q,"rect":{"x":%d,"y":%d,"w":%d,"h":%d},"format":"png","hash_only":false}`,
		ctx, winX, winY, winW, winH))
	m = captureDataMap(t, env)
	if got := m["source"]; got != protocol.CaptureSourceScreenRegion {
		t.Fatalf("region over a window rect reported source=%v, want %q", got, protocol.CaptureSourceScreenRegion)
	}
	if int(m["width"].(float64)) != winW || int(m["height"].(float64)) != winH {
		t.Fatalf("region captured %vx%v, want %dx%d", m["width"], m["height"], winW, winH)
	}
	img = decodeShownImage(t, m)
	if r, g, b, _ := img.At(5, 5).RGBA(); r>>8 != 0x33 || g>>8 != 0x66 || b>>8 != 0xCC {
		t.Fatalf("region pixel (5,5) = #%02x%02x%02x, want the window's #3366cc", r>>8, g>>8, b>>8)
	}

	// Full screen: the connection's own size, read back from the server.
	env = runCapture(t, a, fmt.Sprintf(`{"context":%q,"format":"png","hash_only":true}`, ctx))
	m = captureDataMap(t, env)
	sw, sh := x.Pixels()
	if int(m["width"].(float64)) != sw || int(m["height"].(float64)) != sh {
		t.Fatalf("screen capture %vx%v, want the server's %dx%d", m["width"], m["height"], sw, sh)
	}
	if got := m["source"]; got != protocol.CaptureSourceScreenRegion {
		t.Fatalf("screen capture source = %v, want %q", got, protocol.CaptureSourceScreenRegion)
	}
}

// anyInt reads a detail value that may be the in-memory int or its decoded
// float64 form, depending on whether the envelope was re-parsed.
func anyInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return -1
}

func decodeShownImage(t *testing.T, m map[string]interface{}) image.Image {
	t.Helper()
	b64, _ := m["data"].(string)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("data is not base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("data is not a png: %v", err)
	}
	return img
}

// x11TestWindow creates, colours and maps one override-redirect window with no
// window manager involved. §10's capture needs no WM (GetImage is a drawable
// read), so the test creates the id it captures instead of looking one up.
func x11TestWindow(t *testing.T, x *x11Conn, wx, wy, w, h int, bg uint32) uint32 {
	t.Helper()
	// A resource id must fall inside this client's own id range (setup reply);
	// the server refuses anything else with BadIDChoice.
	id := x.idBase | 1

	// CreateWindow: hdr + wid,parent,x/y,w/h,border,class,visual,mask.
	req := make([]byte, 40) // 10 words: header + 8 fixed words + 2 values
	req[0] = 1   // CreateWindow
	req[1] = 0   // depth = CopyFromParent
	binary.LittleEndian.PutUint16(req[2:4], 10)
	binary.LittleEndian.PutUint32(req[4:8], id)
	binary.LittleEndian.PutUint32(req[8:12], x.root)
	binary.LittleEndian.PutUint16(req[12:14], uint16(int16(wx)))
	binary.LittleEndian.PutUint16(req[14:16], uint16(int16(wy)))
	binary.LittleEndian.PutUint16(req[16:18], uint16(w))
	binary.LittleEndian.PutUint16(req[18:20], uint16(h))
	binary.LittleEndian.PutUint16(req[20:22], 0) // border width
	binary.LittleEndian.PutUint16(req[22:24], 1) // InputOutput
	binary.LittleEndian.PutUint32(req[24:28], 0) // CopyFromParent visual
	binary.LittleEndian.PutUint32(req[28:32], 0x2|0x200) // CWBackPixel | CWOverrideRedirect
	binary.LittleEndian.PutUint32(req[32:36], bg)
	binary.LittleEndian.PutUint32(req[36:40], 1)
	if _, err := x.c.Write(req); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// MapWindow + GetInputFocus as the sync: a round trip that surfaces a
	// queued X error before the capture runs.
	mapReq := make([]byte, 8)
	mapReq[0] = 8 // MapWindow
	binary.LittleEndian.PutUint16(mapReq[2:4], 2)
	binary.LittleEndian.PutUint32(mapReq[4:8], id)
	if _, err := x.c.Write(mapReq); err != nil {
		t.Fatalf("MapWindow: %v", err)
	}
	sync := make([]byte, 4)
	sync[0] = 43 // GetInputFocus
	binary.LittleEndian.PutUint16(sync[2:4], 1)
	if _, err := x.c.Write(sync); err != nil {
		t.Fatalf("sync: %v", err)
	}
	hdr := make([]byte, 32)
	if _, err := io.ReadFull(x.c, hdr); err != nil {
		t.Fatalf("sync reply: %v", err)
	}
	if hdr[0] == 0 {
		t.Fatalf("X error %d creating/mapping the test window", hdr[1])
	}
	if g, err := x.Geometry(id); err != nil || g.W != w || g.H != h {
		t.Fatalf("test window measured %+v (err %v), want %dx%d", g, err, w, h)
	}
	return id
}
