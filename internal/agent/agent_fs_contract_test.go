package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-06 — the fs-* field set P8-12 found incomplete: every response carries
// sha256 + sha256_scope, fs-read always declares next_offset, fs-list counts
// entries instead of a size. These run against the real filesystem.

func TestFSReadWindowCarriesNextOffsetAndIsContiguous(t *testing.T) {
	a := fsAgentOnDisk(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "log.bin")
	payload := make([]byte, 5000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	read := func(offset, limit int) protocol.FSReadResult {
		resp := a.handleFSRead(fsEnvelope(t, protocol.TypeFSRead,
			fmt.Sprintf(`{"path":%q,"offset":%d,"limit":%d}`, path, offset, limit)))
		if resp.Error != nil {
			t.Fatalf("fs-read offset=%d: %s (%s)", offset, resp.Error.Code, resp.Error.Message)
		}
		var res protocol.FSReadResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return res
	}

	first := read(0, 100)
	if !first.Truncated {
		t.Fatal("truncated = false on a 100-byte window of a 5000-byte file")
	}
	if first.Sha256Scope != "window" {
		t.Fatalf("sha256_scope = %q, want \"window\"", first.Sha256Scope)
	}
	if first.NextOffset != 100 {
		t.Fatalf("next_offset = %d, want 100", first.NextOffset)
	}
	if first.Sha256 != sha256Hex(payload[:100]) {
		t.Fatalf("sha256 = %q, want the window digest", first.Sha256)
	}

	second := read(int(first.NextOffset), 100)
	if second.Offset != int(first.NextOffset) {
		t.Fatalf("second window offset = %d, want %d (gap or overlap)", second.Offset, first.NextOffset)
	}
	if second.Sha256 != sha256Hex(payload[100:200]) {
		t.Fatal("second window is not the contiguous next 100 bytes")
	}
	if second.NextOffset != 200 {
		t.Fatalf("second next_offset = %d, want 200", second.NextOffset)
	}

	// A read that reaches EOF must not claim truncation.
	tail := read(4900, 200)
	if tail.Truncated {
		t.Fatal("truncated = true on the final window")
	}
	if tail.NextOffset != 5000 {
		t.Fatalf("final next_offset = %d, want 5000", tail.NextOffset)
	}
}

func TestFSListCarriesListingDigestAndEntryCount(t *testing.T) {
	a := fsAgentOnDisk(t)
	dir := t.TempDir()
	for i, name := range []string{"a.log", "b.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{byte(i)}, 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	resp := a.handleFSList(fsEnvelope(t, protocol.TypeFSList, fmt.Sprintf(`{"path":%q}`, dir)))
	if resp.Error != nil {
		t.Fatalf("fs-list: %s", resp.Error.Message)
	}
	var res protocol.FSListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Sha256Scope != "listing" {
		t.Fatalf("sha256_scope = %q, want \"listing\"", res.Sha256Scope)
	}
	if res.Sha256 == "" {
		t.Fatal("fs-list carries no sha256")
	}
	if res.EntryCount != len(res.Entries) || res.EntryCount != res.Count {
		t.Fatalf("entry_count=%d count=%d len(entries)=%d — must all agree", res.EntryCount, res.Count, len(res.Entries))
	}
	canonical, _ := json.Marshal(res.Entries)
	sum := sha256.Sum256(canonical)
	if res.Sha256 != fmt.Sprintf("%x", sum) {
		t.Fatal("listing sha256 does not cover the canonical JSON of the entries it returned")
	}
}

func TestFSStatCarriesMetadataDigest(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "item.txt")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := a.handleFSStat(fsEnvelope(t, protocol.TypeFSStat, fmt.Sprintf(`{"path":%q}`, path)))
	if resp.Error != nil {
		t.Fatalf("fs-stat: %s", resp.Error.Message)
	}
	var res protocol.FSStatResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Sha256 == "" || res.Sha256Scope == "" {
		t.Fatalf("fs-stat missing proof: sha256=%q scope=%q", res.Sha256, res.Sha256Scope)
	}
	stamp := res
	stamp.Sha256, stamp.Sha256Scope = "", ""
	canonical, _ := json.Marshal(stamp)
	sum := sha256.Sum256(canonical)
	if res.Sha256 != fmt.Sprintf("%x", sum) {
		t.Fatal("fs-stat sha256 does not cover the record it returned")
	}
	if !res.Exists || res.Size != 3 {
		t.Fatalf("stat moved: exists=%v size=%d", res.Exists, res.Size)
	}
}

func TestFSHashCarriesFileScope(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "x.bin")
	payload := []byte("hash me")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := a.handleFSHash(fsEnvelope(t, protocol.TypeFSHash, fmt.Sprintf(`{"path":%q}`, path)))
	if resp.Error != nil {
		t.Fatalf("fs-hash: %s", resp.Error.Message)
	}
	var res protocol.FSHashResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Sha256Scope != "file" {
		t.Fatalf("sha256_scope = %q, want \"file\"", res.Sha256Scope)
	}
	if res.Hash != sha256Hex(payload) {
		t.Fatal("fs-hash digest is not the file digest")
	}
}

func TestFSWriteCarriesFileScope(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "w.bin")
	payload := []byte("written bytes")
	res := decodeWrite(t, a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": base64.StdEncoding.EncodeToString(payload),
	})))
	if res.Sha256Scope != "file" {
		t.Fatalf("sha256_scope = %q, want \"file\" (digest is the file on disk)", res.Sha256Scope)
	}
	if res.Sha256 != sha256Hex(payload) {
		t.Fatal("fs-write digest is not the file digest")
	}
}
