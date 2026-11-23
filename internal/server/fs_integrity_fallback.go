package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-18 (remediation of P8-17 C1) — server-side fs-* field synthesis.
//
// P8-06 claimed that a v1.18.6 agent cannot return a 3-of-5 field set because
// the server fills what the agent does not. That synthesis only existed inside
// the `if truncated` branch of fs-read, a branch a pre-1.19 agent never
// reaches (it does not set truncated). This file is the missing fallback: it
// runs on every fs-* response and fills an absent field from data the server
// actually holds.
//
// Rules:
//   - fill only what is ABSENT. A v1.19.0 agent's own values are never
//     overwritten, so the new-agent wire is unchanged (P8-18 A2).
//   - derive from evidence, never from the request: fs-read digests the decoded
//     window bytes the server just received; fs-list/fs-stat digest the record
//     that was returned; fs-write asks the agent for the file digest after the
//     write (a real read-back), because the server never holds those bytes.
//   - a field the server cannot derive is left absent and is a DOCUMENTED
//     VERSION-GATED GAP (fs-read total_size/truncated on a pre-1.19 agent: an
//     old agent reports neither, and the server cannot distinguish a file that
//     ends exactly at the cap from one that continues).
func (s *Server) applyFSIntegrityFallback(agentID, action, reqPath string, resp interface{}) interface{} {
	m, ok := resp.(map[string]interface{})
	if !ok {
		return resp
	}
	if _, hasErr := m["error"]; hasErr {
		return resp
	}
	switch action {
	case "fs-read":
		fillFSReadIntegrity(m)
	case "fs-list":
		fillFSListingIntegrity(m)
	case "fs-stat":
		fillFSStatIntegrity(m)
	case "fs-hash":
		fillFSHashScope(m)
	case "fs-write":
		s.fillFSWriteIntegrity(agentID, reqPath, m)
	}
	return resp
}

// fillFSReadIntegrity digests the window the server just received. The window
// length is the DECODED byte count: the older code used len(base64)/4*3, an
// upper bound that is wrong by up to two bytes and would hand a tailer an
// overlapping or gapped next_offset.
func fillFSReadIntegrity(m map[string]interface{}) {
	encoded, ok := m["data"].(string)
	if !ok {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	if _, has := m["sha256"]; !has {
		m["sha256"] = fmt.Sprintf("%x", sum)
	}
	if _, has := m["sha256_scope"]; !has {
		m["sha256_scope"] = "window"
	}
	if _, has := m["size"]; !has {
		m["size"] = len(raw)
	}
	if _, has := m["encoding"]; !has {
		m["encoding"] = "base64"
	}
	offset := 0
	if v, ok := m["offset"].(float64); ok {
		offset = int(v)
	}
	if _, has := m["next_offset"]; !has {
		m["next_offset"] = offset + len(raw)
	}
}

// fillFSListingIntegrity digests the canonical JSON of the entries array that
// was returned, exactly as a v1.19.0 agent does for the same array.
func fillFSListingIntegrity(m map[string]interface{}) {
	entries, has := m["entries"]
	if !has {
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return
	}
	list := []protocol.FSEntry{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return
	}
	canonical, err := json.Marshal(list)
	if err != nil {
		return
	}
	sum := sha256.Sum256(canonical)
	if _, has := m["count"]; !has {
		m["count"] = len(list)
	}
	if _, has := m["entry_count"]; !has {
		m["entry_count"] = len(list)
	}
	if _, has := m["sha256"]; !has {
		m["sha256"] = fmt.Sprintf("%x", sum)
	}
	if _, has := m["sha256_scope"]; !has {
		m["sha256_scope"] = "listing"
	}
}

// fillFSStatIntegrity digests the metadata record itself, over the zero-valued
// digest fields, so the value is a function of the record alone — the same
// construction a v1.19.0 agent uses.
func fillFSStatIntegrity(m map[string]interface{}) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	var rec protocol.FSStatResult
	if err := json.Unmarshal(raw, &rec); err != nil {
		return
	}
	rec.Sha256 = ""
	rec.Sha256Scope = ""
	canonical, err := json.Marshal(rec)
	if err != nil {
		return
	}
	sum := sha256.Sum256(canonical)
	if _, has := m["sha256"]; !has {
		m["sha256"] = fmt.Sprintf("%x", sum)
	}
	if _, has := m["sha256_scope"]; !has {
		m["sha256_scope"] = "listing"
	}
}

// fillFSHashScope declares what an already-present digest covers. The digest
// itself is the agent's; only the scope label was missing before 1.19.0.
func fillFSHashScope(m map[string]interface{}) {
	if sum, ok := m["sha256"].(string); !ok || sum == "" {
		return
	}
	if _, has := m["sha256_scope"]; !has {
		m["sha256_scope"] = "file"
	}
}

// fillFSWriteIntegrity reads the file digest back from the agent after the
// write. The server never holds the written bytes, so this is the only sound
// derivation: the digest is the agent's own post-write read-back, and
// verified=true records exactly that (digest read from disk after the write).
func (s *Server) fillFSWriteIntegrity(agentID, reqPath string, m map[string]interface{}) {
	if sum, ok := m["sha256"].(string); ok && sum != "" {
		return
	}
	path, _ := m["path"].(string)
	if path == "" {
		path = reqPath
	}
	if path == "" {
		return
	}
	hr, err := s.forwardToAgent(agentID, protocol.TypeFSHash, protocol.FSParams{Path: path})
	if err != nil {
		return
	}
	hm, ok := hr.(map[string]interface{})
	if !ok {
		return
	}
	sum, _ := hm["sha256"].(string)
	if sum == "" {
		return
	}
	m["sha256"] = sum
	m["sha256_scope"] = "file"
	if _, has := m["size"]; !has {
		if sz, ok := hm["size"]; ok {
			m["size"] = sz
		}
	}
	if _, has := m["verified"]; !has {
		m["verified"] = true
	}
}
