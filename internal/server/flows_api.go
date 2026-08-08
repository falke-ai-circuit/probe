package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// handleV1ListFlows returns all flows.
func (s *Server) handleV1ListFlows(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	flows := s.flows.List()
	writeJSON(w, http.StatusOK, flows)
}

// handleV1GetFlow returns a single flow by ID.
func (s *Server) handleV1GetFlow(w http.ResponseWriter, r *http.Request, flowID string) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	flow, err := s.flows.Get(flowID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, flow)
}

// handleV1CreateFlow creates a new flow.
func (s *Server) handleV1CreateFlow(w http.ResponseWriter, r *http.Request) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Trigger     FlowTrigger `json:"trigger"`
		Steps       []FlowStep  `json:"steps"`
		AgentIDs    []string    `json:"agent_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	operatorID := ""
	if op != nil {
		operatorID = op.ID
	}
	flow, err := s.flows.Create(req.Name, req.Description, req.Trigger, req.Steps, req.AgentIDs, operatorID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if s.audit != nil {
		s.audit.Log(AuditEntry{
			FlowID:     flow.ID,
			EventType:  "create",
			Action:     "flow.create",
			Params:     map[string]string{"flow_id": flow.ID, "name": flow.Name},
			Result:     "success",
			OperatorID: operatorID,
		})
	}

	writeJSON(w, http.StatusOK, flow)
}

// handleV1UpdateFlow updates an existing flow.
func (s *Server) handleV1UpdateFlow(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Trigger     *FlowTrigger `json:"trigger"`
		Steps       []FlowStep   `json:"steps"`
		AgentIDs    []string     `json:"agent_ids"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	flow, err := s.flows.Update(flowID, req.Name, req.Description, req.Trigger, req.Steps, req.AgentIDs)
	if err != nil {
		if err.Error() == "flow "+flowID+" not found" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}

	if s.audit != nil {
		operatorID := ""
		if op != nil {
			operatorID = op.ID
		}
		s.audit.Log(AuditEntry{
			AgentID:    "",
			OperatorID: operatorID,
			Action:     "flow.update",
			Params:     map[string]string{"flow_id": flow.ID},
			Result:     "success",
		})
	}

	writeJSON(w, http.StatusOK, flow)
}

// handleV1DeleteFlow deletes a flow.
func (s *Server) handleV1DeleteFlow(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	if err := s.flows.Delete(flowID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	if s.audit != nil {
		operatorID := ""
		if op != nil {
			operatorID = op.ID
		}
		s.audit.Log(AuditEntry{
			AgentID:    "",
			OperatorID: operatorID,
			Action:     "flow.delete",
			Params:     map[string]string{"flow_id": flowID},
			Result:     "success",
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"deleted": flowID})
}

// handleV1EnableFlow enables a flow.
func (s *Server) handleV1EnableFlow(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	if err := s.flows.Enable(flowID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		operatorID := ""
		if op != nil {
			operatorID = op.ID
		}
		s.audit.Log(AuditEntry{
			AgentID:    "",
			OperatorID: operatorID,
			Action:     "flow.enable",
			Params:     map[string]string{"flow_id": flowID},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"flow_id": flowID, "enabled": "true"})
}

// handleV1DisableFlow disables a flow.
func (s *Server) handleV1DisableFlow(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	if err := s.flows.Disable(flowID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		operatorID := ""
		if op != nil {
			operatorID = op.ID
		}
		s.audit.Log(AuditEntry{
			AgentID:    "",
			OperatorID: operatorID,
			Action:     "flow.disable",
			Params:     map[string]string{"flow_id": flowID},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"flow_id": flowID, "enabled": "false"})
}

// handleV1FlowRunNow creates a FlowRun for immediate execution.
func (s *Server) handleV1FlowRunNow(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
	}

	operatorID := ""
	if op != nil {
		operatorID = op.ID
	}
	run, err := s.flows.RunNow(flowID, req.AgentID, operatorID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	// Dispatch asynchronously if dispatcher is bound
	if s.flowDispatcher != nil && req.AgentID != "" {
		flow, _ := s.flows.Get(flowID)
		if flow != nil {
			s.flowDispatcher.DispatchRun(run, flow)
		}
	}

	if s.audit != nil {
		s.audit.Log(AuditEntry{
			AgentID:    req.AgentID,
			OperatorID: operatorID,
			Action:     "flow.run_now",
			Params:     map[string]string{"flow_id": flowID, "run_id": run.ID},
			Result:     "success",
		})
	}

	writeJSON(w, http.StatusOK, run)
}

// handleV1ListFlowRuns returns runs matching optional filter.
func (s *Server) handleV1ListFlowRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	filter := RunFilter{
		FlowID:  r.URL.Query().Get("flow_id"),
		AgentID: r.URL.Query().Get("agent_id"),
		Status:  r.URL.Query().Get("status"),
	}
	runs := s.flows.ListRuns(filter)
	writeJSON(w, http.StatusOK, runs)
}

// handleV1GetFlowRun returns a single run by ID.
func (s *Server) handleV1GetFlowRun(w http.ResponseWriter, r *http.Request, runID string) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	run, err := s.flows.GetRun(runID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleV1ListAgentFlows returns flows assigned to a specific agent.
func (s *Server) handleV1ListAgentFlows(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	flows := s.flows.ListFlowsForAgent(agentID)
	writeJSON(w, http.StatusOK, flows)
}

// handleV1AssignFlowToAgent assigns a flow to an agent.
func (s *Server) handleV1AssignFlowToAgent(w http.ResponseWriter, r *http.Request, flowID string) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.AgentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id is required"})
		return
	}

	if err := s.flows.AssignFlowToAgent(flowID, req.AgentID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	if s.audit != nil {
		operatorID := ""
		if op != nil {
			operatorID = op.ID
		}
		s.audit.Log(AuditEntry{
			AgentID:    req.AgentID,
			OperatorID: operatorID,
			Action:     "flow.assign",
			Params:     map[string]string{"flow_id": flowID, "agent_id": req.AgentID},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"flow_id": flowID, "agent_id": req.AgentID})
}

// handleV1UnassignFlowFromAgent removes a flow assignment.
func (s *Server) handleV1UnassignFlowFromAgent(w http.ResponseWriter, r *http.Request, flowID string) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow manager not initialized"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	if err := s.flows.UnassignFlowFromAgent(flowID, req.AgentID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"flow_id": flowID, "agent_id": req.AgentID, "removed": "true"})
}

// v1WrapFlowHandler wraps a handler that takes a flowID path parameter.
// Mirrors v1WrapAgentHandler but for flow operations.
func (s *Server) v1WrapFlowHandler(action string, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flowID := r.PathValue("id")
		op, ok := s.v1CheckAuth(w, r, action)
		if !ok {
			return
		}
		// Set operator in context so handlers can retrieve via v1OperatorFromRequest
		if op != nil {
			r = r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, op))
		}
		h(w, r, flowID)
	}
}

// handleV1ListFlowTemplates returns all loaded flow templates.
func (s *Server) handleV1ListFlowTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flowTemplates == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	templates := s.flowTemplates.List()
	writeJSON(w, http.StatusOK, templates)
}

// handleV1InstantiateFromTemplate creates a new flow from a named template.
func (s *Server) handleV1InstantiateFromTemplate(w http.ResponseWriter, r *http.Request) {
	op, ok := s.v1CheckAuth(w, r, "exec")
	if !ok {
		return
	}
	if s.flowTemplates == nil || s.flows == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow templates not available"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}
	defer r.Body.Close()
	var req struct {
		TemplateName string `json:"template_name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.TemplateName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "template_name is required"})
		return
	}
	operatorID := ""
	if op != nil {
		operatorID = op.ID
	}
	flow, err := s.flowTemplates.Instantiate(req.TemplateName, s.flows, operatorID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if s.audit != nil {
		s.audit.Log(AuditEntry{
			OperatorID: operatorID,
			Action:     "flow.from_template",
			Params:     map[string]string{"template": req.TemplateName, "flow_id": flow.ID},
			Result:     "success",
		})
	}
	writeJSON(w, http.StatusOK, flow)
}

// handleV1ListAgentSurveyEvents returns survey events for a specific agent.
func (s *Server) handleV1ListAgentSurveyEvents(w http.ResponseWriter, r *http.Request, agentID string) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.flowEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "flow events not available"})
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	filter := FlowEventFilter{
		AgentID: agentID,
		Limit:   limit,
	}
	if f := r.URL.Query().Get("flow_id"); f != "" {
		filter.FlowID = f
	}
	if s := r.URL.Query().Get("signal"); s != "" {
		filter.Signal = s
	}
	events, err := s.flowEvents.Query(filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, events)
}
