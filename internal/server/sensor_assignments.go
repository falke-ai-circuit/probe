package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)
// v1WrapSensorHandler wraps a handler that takes agentID + sensorName
// path parameters. Mirrors v1WrapFlowHandler and v1WrapAgentHandler.
func (s *Server) v1WrapSensorHandler(action string, h func(http.ResponseWriter, *http.Request, string, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		sensorName := r.PathValue("name")
		if _, ok := s.v1CheckAuth(w, r, action); !ok {
			return
		}
		h(w, r, agentID, sensorName)
	}
}



// SensorAssignment maps sensor names to per-agent enable/config. Each
// agent gets a list of which sensors are enabled and their per-sensor
// argument defaults.
type SensorAssignment struct {
	AgentID string                 `json:"agent_id"`
	Sensors map[string]SensorState `json:"sensors"`
}

// SensorState is one sensor's assignment: enabled/disabled plus default
// args. Args are passed to the sensor's Read function.
type SensorState struct {
	Enabled bool              `json:"enabled"`
	Args    map[string]string `json:"args,omitempty"`
}

// SensorAssignmentManager persists per-agent sensor assignments to disk.
// Storage layout: one JSON file per agent in the assignment dir.
type SensorAssignmentManager struct {
	mu     sync.Mutex
	byID   map[string]*SensorAssignment
	path   string
}

// NewSensorAssignmentManager loads or creates the assignment store.
func NewSensorAssignmentManager(path string) *SensorAssignmentManager {
	m := &SensorAssignmentManager{
		byID: make(map[string]*SensorAssignment),
		path: path,
	}
	m.load()
	return m
}

func (m *SensorAssignmentManager) load() {
	data, err := readFile(m.path)
	if err != nil {
		return
	}
	var all map[string]*SensorAssignment
	if err := json.Unmarshal(data, &all); err != nil {
		return
	}
	m.byID = all
}

// Get returns the assignment for an agent (or a default empty one if not set).
func (m *SensorAssignmentManager) Get(agentID string) *SensorAssignment {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byID[agentID]; ok {
		return a
	}
	return &SensorAssignment{AgentID: agentID, Sensors: map[string]SensorState{}}
}

// Set replaces the assignment for an agent.
func (m *SensorAssignmentManager) Set(a *SensorAssignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Sensors == nil {
		a.Sensors = map[string]SensorState{}
	}
	m.byID[a.AgentID] = a
	return m.saveLocked()
}

// EnableSensor sets one sensor to enabled for an agent.
func (m *SensorAssignmentManager) EnableSensor(agentID, sensorName string, args map[string]string) error {
	a := m.Get(agentID)
	a.Sensors[sensorName] = SensorState{Enabled: true, Args: args}
	return m.Set(a)
}

// DisableSensor sets one sensor to disabled for an agent.
func (m *SensorAssignmentManager) DisableSensor(agentID, sensorName string) error {
	a := m.Get(agentID)
	a.Sensors[sensorName] = SensorState{Enabled: false}
	return m.Set(a)
}

func (m *SensorAssignmentManager) saveLocked() error {
	data, err := json.MarshalIndent(m.byID, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(m.path, data)
}

// readFile / writeFile are package-level helpers in flows.go (reused).
// They're defined as small wrappers around os.ReadFile/WriteFile so we
// can stub them in tests.

// handleV1GetAgentSensorsRaw is the top-level route handler. It does auth,
// then delegates to handleV1GetAgentSensors. Registered directly (without
// v1WrapAgentHandler) to avoid the double-wrap response bug.
func (s *Server) handleV1GetAgentSensorsRaw(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	agentID := r.PathValue("id")
	s.handleV1GetAgentSensors(w, r, agentID)
}

// handleV1SetAgentSensorsRaw is the top-level PUT handler — same pattern as
// the GET. Does auth, then delegates.
func (s *Server) handleV1SetAgentSensorsRaw(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	agentID := r.PathValue("id")
	s.handleV1SetAgentSensors(w, r, agentID)
}

// handleV1GetAgentSensors returns the sensor assignment for a given agent.
// Writes a raw JSON body — NOT wrapped via writeJSON — because this route
// is registered via the raw handler above which does NOT add an APIResponse
// envelope. (Reviewer #1 — double-wrap fix.)
func (s *Server) handleV1GetAgentSensors(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.sensorAssign == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sensor assignment not available"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(s.sensorAssign.Get(agentID))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":true,"data":%s}`, string(body))))
}

// handleV1SetAgentSensors replaces the sensor assignment for an agent.
// Writes a raw JSON body for the same reason as handleV1GetAgentSensors.
func (s *Server) handleV1SetAgentSensors(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if s.sensorAssign == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sensor assignment not available"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var a SensorAssignment
	if err := json.Unmarshal(body, &a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	operatorID := ""
	if op := r.Context().Value(operatorContextKey{}); op != nil {
		if o, ok := op.(*Operator); ok {
			operatorID = o.ID
		}
	}
	a.AgentID = agentID
	if err := s.sensorAssign.Set(&a); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		s.audit.Log(AuditEntry{
			AgentID:    agentID,
			OperatorID: operatorID,
			Action:     "sensor.assign",
			Params:     map[string]string{"agent_id": agentID, "count": fmt.Sprintf("%d", len(a.Sensors))},
			Result:     "success",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	bodyBytes, _ := json.Marshal(&a)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"ok":true,"data":%s}`, string(bodyBytes))))
}

// handleV1EnableSensor enables a single sensor for an agent.
func (s *Server) handleV1EnableSensor(w http.ResponseWriter, r *http.Request, agentID, sensorName string) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if s.sensorAssign == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sensor assignment not available"})
		return
	}
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var req struct {
		Args map[string]string `json:"args"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	if err := s.sensorAssign.EnableSensor(agentID, sensorName, req.Args); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		s.audit.Log(AuditEntry{
			AgentID:    agentID,
			Action:     "sensor.enable",
			Params:     map[string]string{"agent_id": agentID, "sensor": sensorName},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"enabled": "true", "sensor": sensorName, "agent_id": agentID})
}

// handleV1DisableSensor disables a single sensor for an agent.
func (s *Server) handleV1DisableSensor(w http.ResponseWriter, r *http.Request, agentID, sensorName string) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if s.sensorAssign == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sensor assignment not available"})
		return
	}
	if err := s.sensorAssign.DisableSensor(agentID, sensorName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		s.audit.Log(AuditEntry{
			AgentID:    agentID,
			Action:     "sensor.disable",
			Params:     map[string]string{"agent_id": agentID, "sensor": sensorName},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"enabled": "false", "sensor": sensorName, "agent_id": agentID})
}
