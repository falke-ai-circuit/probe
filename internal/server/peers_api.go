package server

// P17-02 (spec §2) — /api/v1/peers CRUD + /action — the six routes.
//
// Persistence order is BINDING (FM-2.1): seal → doc write → live apply.
// A credential-store failure fails the request 500 PEER_STORE_INIT_FAILED
// and NOTHING is half-persisted.FM-2.3: default_peers is touched only by
// these handlers under the store lock; runtime rows (peers map) only by the
// relay/inventory code. §6: the token appears once in a POST/PUT body, is
// never echoed, never logged, never in an error.
//
// Errors are the typed envelope: PEER_EXISTS 409, PEER_NOT_FOUND 404,
// PEER_UPSTREAM_INVALID 400, PEER_ID_INVALID 400, PEER_TOKEN_REQUIRED 400,
// PEER_STORE_INIT_FAILED 500.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// registerPeersRoutes wires the six routes (called from registerV1Routes).
func (s *Server) registerPeersRoutes() {
	s.mux.HandleFunc("GET /api/v1/peers", s.handleV1ListPeers)
	s.mux.HandleFunc("POST /api/v1/peers", s.handleV1CreatePeer)
	s.mux.HandleFunc("GET /api/v1/peers/{id}", s.handleV1GetPeer)
	s.mux.HandleFunc("PUT /api/v1/peers/{id}", s.handleV1PutPeer)
	s.mux.HandleFunc("DELETE /api/v1/peers/{id}", s.handleV1DeletePeer)
	s.mux.HandleFunc("POST /api/v1/peers/{id}/action", s.handleV1PeerAction)
}

// peerBody is the POST/PUT request shape. Token is write-only (§6).
// Uplink (P25-05R) marks the peer as the server's forward uplink; the
// wiring fires when the peer adopts, not at config time.
// P27-02 (spec P27-01 §3.1/D3): the three optional dial-discipline keys ride
// the SAME body — UI-added and API-added peers are the same stored shape by
// construction (D3). Empty UplinkType reads always-on (legacy records).
type peerBody struct {
	ID            string `json:"id"`
	Upstream      string `json:"upstream"`
	Token         string `json:"token"`
	Enabled       *bool  `json:"enabled"`
	Uplink        *bool  `json:"uplink"`
	Listen        string `json:"listen"`
	MaxAgents     int    `json:"max_agents"`
	UplinkType    string `json:"uplink_type"`
	ScheduleStart string `json:"schedule_start"`
	ScheduleEnd   string `json:"schedule_end"`
}

// validateUplinkKeys enforces the §3.1 rules on the optional keys:
// unknown uplink_type ⇒ PEER_UPLINK_TYPE_INVALID; discipline on a
// listen-set (inbound) peer ⇒ PEER_UPLINK_TYPE_UNSUPPORTED (an inbound
// listener has no dial semantics); scheduled requires a valid HH:MM pair ⇒
// PEER_SCHEDULE_INVALID.
func validateUplinkKeys(t string, listen string, start, end string) (string, error) {
	ty := strings.TrimSpace(t)
	if ty == "" {
		ty = uplinkTypeAlwaysOn
	}
	if !validUplinkType(ty) {
		return "", fmt.Errorf("PEER_UPLINK_TYPE_INVALID")
	}
	if strings.TrimSpace(listen) != "" && ty != uplinkTypeAlwaysOn {
		return "", fmt.Errorf("PEER_UPLINK_TYPE_UNSUPPORTED")
	}
	if ty == uplinkTypeScheduled {
		if err := validateScheduleWindow(start, end); err != nil {
			return "", fmt.Errorf("PEER_SCHEDULE_INVALID")
		}
	}
	return ty, nil
}

// loadDocForPeers loads (under the store lock) and returns with lock still
// held. Caller MUST unlock.
func (s *Server) loadDocForPeers() *serversStore {
	st := s.inventory()
	st.load()
	st.mu.Lock()
	st.loadLocked()
	return st
}

// writeDocForPeers releases the store lock with a forced write (state
// transition persists at once).
func writeDocForPeers(st *serversStore) {
	st.dirty = true
	st.writeLocked(true)
	st.mu.Unlock()
}

// handleV1ListPeers answers GET /api/v1/peers.
func (s *Server) handleV1ListPeers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	pm := s.peerManagerFor()
	writeJSON(w, http.StatusOK, map[string]any{"peers": pm.snapshot()})
}

// handleV1GetPeer answers GET /api/v1/peers/{id}.
func (s *Server) handleV1GetPeer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	id := r.PathValue("id")
	pm := s.peerManagerFor()
	view := pm.configView(id)
	if view == nil {
		writeError(w, http.StatusNotFound, "PEER_NOT_FOUND", "peer "+id+" is not configured")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleV1CreatePeer answers POST /api/v1/peers (spec: 201, 409 duplicate).
// Order BINDING (§2 FM-2.1): validate → seal → doc write → live apply.
func (s *Server) handleV1CreatePeer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "config"); !ok {
		return
	}
	var body peerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	if validatePeerID(body.ID) != nil {
		writeError(w, http.StatusBadRequest, "PEER_ID_INVALID", "peer id must match [A-Za-z0-9._-]{1,64}")
		return
	}
	if err := validatePeerUpstream(body.Upstream); err != nil {
		writeError(w, http.StatusBadRequest, "PEER_UPSTREAM_INVALID", err.Error())
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		writeError(w, http.StatusBadRequest, "PEER_TOKEN_REQUIRED", "token is required on POST")
		return
	}
	// P27-02 (spec §3.1): the optional dial-discipline keys, validated before
	// the seal step (FM-2.1 order preserved: validate → seal → doc → apply).
	ty, terr := validateUplinkKeys(body.UplinkType, body.Listen, body.ScheduleStart, body.ScheduleEnd)
	if terr != nil {
		switch terr.Error() {
		case "PEER_UPLINK_TYPE_INVALID":
			writeError(w, http.StatusBadRequest, "PEER_UPLINK_TYPE_INVALID",
				"uplink_type must be one of: always-on, scheduled, manual")
		case "PEER_UPLINK_TYPE_UNSUPPORTED":
			writeError(w, http.StatusBadRequest, "PEER_UPLINK_TYPE_UNSUPPORTED",
				"an inbound (listen-set) peer has no dial semantics; uplink_type must be always-on")
		default:
			writeError(w, http.StatusBadRequest, "PEER_SCHEDULE_INVALID",
				"uplink_type=scheduled requires schedule_start and schedule_end as HH:MM (server-local; start>end wraps midnight)")
		}
		return
	}
	store, err := s.peerCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PEER_STORE_INIT_FAILED",
			"credential store unavailable: peer not persisted")
		return
	}
	sealed, fp, serr := sealPeerToken(store, body.Token)
	if serr != nil {
		writeError(w, http.StatusInternalServerError, "PEER_STORE_INIT_FAILED",
			"credential store refused the token: peer not persisted")
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	uplink := false
	if body.Uplink != nil {
		uplink = *body.Uplink
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := defaultPeerRecord{
		ID: body.ID, Upstream: strings.TrimSpace(body.Upstream),
		TokenSealed: sealed, TokenFP: fp,
		Enabled: enabled, Uplink: uplink,
		UplinkType:    ty,
		ScheduleStart: strings.TrimSpace(body.ScheduleStart),
		ScheduleEnd:   strings.TrimSpace(body.ScheduleEnd),
		Listen:        strings.TrimSpace(body.Listen),
		MaxAgents:     body.MaxAgents,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	st := s.loadDocForPeers()
	if _, dup := st.doc.DefaultPeers[rec.ID]; dup {
		st.mu.Unlock()
		writeError(w, http.StatusConflict, "PEER_EXISTS", "peer "+rec.ID+" already exists")
		return
	}
	st.doc.DefaultPeers[rec.ID] = &rec
	writeDocForPeers(st) // doc write AFTER seal (FM-2.1)

	s.peerManagerFor().registerConnector(rec) // live apply AFTER doc write
	s.auditPeer(rec.ID, "peer.created", "",
		map[string]any{"upstream": rec.Upstream, "enabled": rec.Enabled, "token_fp": rec.TokenFP})
	if view := s.peerManagerFor().configView(rec.ID); view != nil {
		writeJSON(w, http.StatusCreated, view)
		return
	}
	writeJSON(w, http.StatusCreated, peerConfigView{
		ID: rec.ID, Upstream: rec.Upstream, TokenFP: rec.TokenFP, TokenPresent: true,
		Enabled: rec.Enabled, Listen: rec.Listen, MaxAgents: rec.MaxAgents,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
		LinkState:   "", // disabled: outside the §4 machine
		StateDetail: linkStateDetail{},
	})
}

// handleV1PutPeer answers PUT /api/v1/peers/{id} (full replace; token
// optional — absent keeps stored; response carries the new fp on rotation).
func (s *Server) handleV1PutPeer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "config"); !ok {
		return
	}
	id := r.PathValue("id")
	var body peerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	st := s.loadDocForPeers()
	existing, ok := st.doc.DefaultPeers[id]
	if !ok {
		st.mu.Unlock()
		writeError(w, http.StatusNotFound, "PEER_NOT_FOUND", "peer "+id+" is not configured")
		return
	}
	if err := validatePeerUpstream(body.Upstream); err != nil {
		st.mu.Unlock()
		writeError(w, http.StatusBadRequest, "PEER_UPSTREAM_INVALID", err.Error())
		return
	}
	// P27-02 (spec §3.1): same validation family as POST.
	ty, terr := validateUplinkKeys(body.UplinkType, body.Listen, body.ScheduleStart, body.ScheduleEnd)
	if terr != nil {
		st.mu.Unlock()
		switch terr.Error() {
		case "PEER_UPLINK_TYPE_INVALID":
			writeError(w, http.StatusBadRequest, "PEER_UPLINK_TYPE_INVALID",
				"uplink_type must be one of: always-on, scheduled, manual")
		case "PEER_UPLINK_TYPE_UNSUPPORTED":
			writeError(w, http.StatusBadRequest, "PEER_UPLINK_TYPE_UNSUPPORTED",
				"an inbound (listen-set) peer has no dial semantics; uplink_type must be always-on")
		default:
			writeError(w, http.StatusBadRequest, "PEER_SCHEDULE_INVALID",
				"uplink_type=scheduled requires schedule_start and schedule_end as HH:MM (server-local; start>end wraps midnight)")
		}
		return
	}

	store, err := s.peerCredentials()
	if err != nil {
		st.mu.Unlock()
		writeError(w, http.StatusInternalServerError, "PEER_STORE_INIT_FAILED",
			"credential store unavailable: change not persisted")
		return
	}

	// §6: PUT without token keeps the stored secret. Rotation = PUT with a
	// token; seal BEFORE the doc write (FM-2.1).
	fp := existing.TokenFP
	if strings.TrimSpace(body.Token) != "" {
		sealed, newFP, serr := sealPeerToken(store, body.Token)
		if serr != nil {
			st.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "PEER_STORE_INIT_FAILED",
				"credential store refused the token: change not persisted")
			return
		}
		existing.TokenSealed = sealed
		fp = newFP
	}
	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Uplink != nil {
		existing.Uplink = *body.Uplink
	}
	existing.Upstream = strings.TrimSpace(body.Upstream)
	existing.Enabled = enabled
	existing.UplinkType = ty
	existing.ScheduleStart = strings.TrimSpace(body.ScheduleStart)
	existing.ScheduleEnd = strings.TrimSpace(body.ScheduleEnd)
	existing.Listen = strings.TrimSpace(body.Listen)
	existing.MaxAgents = body.MaxAgents
	existing.TokenFP = fp
	existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	st.doc.DefaultPeers[id] = existing
	writeDocForPeers(st)

	s.peerManagerFor().applyConfig(*existing)
	s.auditPeer(id, "peer.updated", "",
		map[string]any{"upstream": existing.Upstream, "enabled": existing.Enabled, "token_fp": fp})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "upstream": existing.Upstream, "token_fp": fp,
		"token_present": true, "enabled": existing.Enabled,
		"listen": existing.Listen, "max_agents": existing.MaxAgents,
		"uplink_type":    existing.effectiveUplinkType(),
		"schedule_start": existing.ScheduleStart, "schedule_end": existing.ScheduleEnd,
	})
}

// handleV1DeletePeer answers DELETE /api/v1/peers/{id} (204; closes the
// uplink; runtime peerRecord follows its own retention per P22-02).
func (s *Server) handleV1DeletePeer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "config"); !ok {
		return
	}
	id := r.PathValue("id")
	st := s.loadDocForPeers()
	rec, ok := st.doc.DefaultPeers[id]
	if !ok {
		st.mu.Unlock()
		writeError(w, http.StatusNotFound, "PEER_NOT_FOUND", "peer "+id+" is not configured")
		return
	}
	delete(st.doc.DefaultPeers, id)
	if len(st.doc.DefaultPeers) == 0 {
		// Keep the document at v1 semantics when the namespace empties —
		// an old binary reading a v2 doc survives it, but honesty says an
		// empty namespace should not pin the bump.
		st.doc.Version = DefaultPeersDocVersion
	}
	writeDocForPeers(st)

	s.peerManagerFor().stopRuntime(id) // closes the uplink live (FM-1.3)
	s.auditPeer(id, "peer.deleted", "", map[string]any{"upstream": rec.Upstream})
	w.WriteHeader(http.StatusNoContent)
}

// handleV1PeerAction answers POST /api/v1/peers/{id}/action with
// {"action":"enable"|"disable"|"dial-now"}.
func (s *Server) handleV1PeerAction(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "config"); !ok {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	st := s.loadDocForPeers()
	rec, ok := st.doc.DefaultPeers[id]
	if !ok {
		st.mu.Unlock()
		writeError(w, http.StatusNotFound, "PEER_NOT_FOUND", "peer "+id+" is not configured")
		return
	}
	switch body.Action {
	case "enable":
		if rec.Enabled {
			st.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "already enabled"})
			return
		}
		rec.Enabled = true
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		st.doc.DefaultPeers[id] = rec
		writeDocForPeers(st)
		s.peerManagerFor().applyConfig(*rec)
		s.auditPeer(id, "peer.updated", "", map[string]any{"enabled": true})
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "enabled"})
	case "disable":
		if !rec.Enabled {
			st.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "already disabled"})
			return
		}
		rec.Enabled = false
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		st.doc.DefaultPeers[id] = rec
		writeDocForPeers(st)
		s.peerManagerFor().applyConfig(*rec)
		s.auditPeer(id, "peer.updated", "", map[string]any{"enabled": false})
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "disabled"})
	case "dial-now":
		st.mu.Unlock()
		// §2: immediate reconnect, backoff reset. Only meaningful on an
		// enabled peer; the manager ignores unknown ids.
		if !s.peerManagerFor().dialNow(id) {
			writeError(w, http.StatusConflict, "PEER_NOT_RUNNING",
				"peer "+id+" has no live lifecycle to redial")
			return
		}
		s.auditPeer(id, "peer.state_changed", "", map[string]any{"action": "dial-now"})
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "dialing"})
	case "dial":
		// P27-02 (spec P27-01 §3.3): the ONLY way a manual uplink ever
		// connects. Semantics per discipline (checked under the doc lock
		// BEFORE any state change):
		//   manual    — a single bounded dial via the action path; connected
		//               ⇒ 409 PEER_ALREADY_CONNECTED; failure ⇒ unreachable
		//               with the refusal carried verbatim.
		//   scheduled — an in-window kick of the existing loop is allowed
		//               (idempotent, FM-1.3 no-op-if-connected); outside the
		//               window ⇒ 409 SCHEDULE_WINDOW_CLOSED.
		//   always-on — dial is the loop's own job; refused 409
		//               PEER_NOT_MANUAL (the operator has nothing to kick).
		ty := rec.effectiveUplinkType()
		switch ty {
		case uplinkTypeAlwaysOn:
			st.mu.Unlock()
			writeError(w, http.StatusConflict, "PEER_NOT_MANUAL",
				"peer "+id+" is always-on; its dial loop needs no manual kick")
			return
		case uplinkTypeScheduled:
			if !rec.inWindowNow() {
				st.mu.Unlock()
				writeError(w, http.StatusConflict, "SCHEDULE_WINDOW_CLOSED",
					"peer "+id+" is scheduled and outside its window")
				return
			}
			st.mu.Unlock()
			if !s.peerManagerFor().dialNow(id) {
				writeError(w, http.StatusConflict, "PEER_NOT_RUNNING",
					"peer "+id+" has no live lifecycle to redial")
				return
			}
			s.auditPeer(id, "peer.state_changed", "", map[string]any{"action": "dial", "uplink_type": ty})
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": "dialing"})
			return
		default: // manual
			st.mu.Unlock()
			verr := s.peerManagerFor().manualDial(id)
			if verr != nil {
				writeJSON(w, http.StatusConflict, map[string]any{
					"id": id, "state": peerStateUnreachable, "error": verr.Error(),
				})
				return
			}
			s.auditPeer(id, "peer.state_changed", "", map[string]any{"action": "dial", "uplink_type": ty})
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": peerStateConnected})
		}
	default:
		st.mu.Unlock() // error paths release the lock they took (the leak class)
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"action must be one of: enable, disable, dial-now, dial")
	}
}
