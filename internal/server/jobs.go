package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-04 — server-side witness for detached jobs.
//
// The server can never execute cleanup on a host whose agent is gone: it has
// no process there. The guardian is the executor; the server only keeps a
// record of what it launched and marks a job recovery_required when the agent
// drops with the job still open and no guardian report arrives within 120s.
// It never invents output for a job it did not observe — every field it stores
// came from a launch response or a job_status answer read from the spool.

// jobRecoveryWindow is how long a dropped agent's open job may stay silent
// before the record is escalated to recovery_required.
const jobRecoveryWindow = 120 * time.Second

// ServerJob is the server's record of one detached job.
type ServerJob struct {
	JobID            string `json:"job_id"`
	AgentID          string `json:"agent_id"`
	PID              int    `json:"pid"`
	StartedAtUTC     string `json:"started_at_utc"`
	Status           string `json:"status"` // open | recovery_pending | recovery_required | resolved
	Phase            string `json:"phase,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	OnDeathRan       bool   `json:"on_death_ran"`
	RecoveryRequired bool   `json:"recovery_required"`
	LastReportUTC    string `json:"last_report_utc,omitempty"`
	Note             string `json:"note,omitempty"`
}

// recordDetachedJob stores a job the agent reported launching. Responses that
// are not a launch handle are ignored, so foreground exec is untouched.
func (s *Server) recordDetachedJob(agentID string, resp interface{}) {
	m, ok := resp.(map[string]interface{})
	if !ok {
		return
	}
	jobID, _ := m["job_id"].(string)
	if jobID == "" {
		return
	}
	pid := 0
	if v, ok := m["pid"].(float64); ok {
		pid = int(v)
	}
	started, _ := m["started_at_utc"].(string)
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[string]*ServerJob)
	}
	s.jobs[jobID] = &ServerJob{
		JobID:        jobID,
		AgentID:      agentID,
		PID:          pid,
		StartedAtUTC: started,
		Status:       "open",
		Phase:        "running",
	}
}

// agentJobsDisconnected marks every open job of a dropped agent as pending
// recovery and arms the 120s escalation.
func (s *Server) agentJobsDisconnected(agentID string) {
	s.jobMu.Lock()
	var pending []string
	for id, j := range s.jobs {
		if j.AgentID != agentID || j.Status == "resolved" {
			continue
		}
		j.Status = "recovery_pending"
		j.Note = "agent disconnected with job open"
		pending = append(pending, id)
	}
	s.jobMu.Unlock()
	for _, id := range pending {
		jobID := id
		time.AfterFunc(jobRecoveryWindow, func() {
			s.jobMu.Lock()
			defer s.jobMu.Unlock()
			j, ok := s.jobs[jobID]
			if !ok || j.Status == "resolved" || j.Status == "recovery_required" {
				return
			}
			j.Status = "recovery_required"
			j.RecoveryRequired = true
			j.Note = fmt.Sprintf("no guardian report %s after agent drop", jobRecoveryWindow)
			log.Printf("[jobs] %s recovery_required (agent %s, no spool report)", jobID, j.AgentID)
		})
	}
}

// reconcileAgentJobs asks a reconnected agent for the spool state of its open
// jobs. The guardian wrote that state, so the answer is authoritative even
// though this agent never saw the job finish.
func (s *Server) reconcileAgentJobs(agentID string) {
	s.jobMu.Lock()
	var ids []string
	for id, j := range s.jobs {
		if j.AgentID == agentID && j.Status != "resolved" {
			ids = append(ids, id)
		}
	}
	s.jobMu.Unlock()
	for _, id := range ids {
		resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeJobStatus,
			protocol.JobStatusParams{JobID: id}, 5*time.Second, "")
		if err != nil {
			log.Printf("[jobs] reconcile %s: %v", id, err)
			continue
		}
		s.applyJobStatus(id, resp)
	}
}

// applyJobStatus folds a spool answer into the server record.
func (s *Server) applyJobStatus(jobID string, resp interface{}) {
	m, ok := resp.(map[string]interface{})
	if !ok {
		return
	}
	phase, _ := m["phase"].(string)
	if phase == "" {
		// Shape from an envelope-wrapped result, if the transport wrapped it.
		if r, ok := m["result"].(map[string]interface{}); ok {
			m = r
			phase, _ = m["phase"].(string)
		}
	}
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok {
		return
	}
	j.Phase = phase
	j.LastReportUTC = time.Now().UTC().Format(time.RFC3339)
	if v, ok := m["exit_code"].(float64); ok {
		code := int(v)
		j.ExitCode = &code
	}
	if v, ok := m["on_death_ran"].(bool); ok {
		j.OnDeathRan = v
	}
	if phase == "closed" {
		j.Status = "resolved"
		j.RecoveryRequired = false
		j.Note = "resolved from spool after reconnect"
	}
}

// handleV1JobStatus forwards job_status and resolves the witness record with
// the same answer.
func (s *Server) handleV1JobStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		if _, ok := s.v1CheckAuth(w, r, "job-status"); !ok {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body")
			return
		}
		r.Body.Close()
		var params protocol.JobStatusParams
		if err := json.Unmarshal(body, &params); err != nil || params.JobID == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "job_id is required")
			return
		}
		resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeJobStatus, params, 15*time.Second, "")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
			return
		}
		s.applyJobStatus(params.JobID, resp)
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleV1JobLog forwards an incremental tail request.
func (s *Server) handleV1JobLog() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		if _, ok := s.v1CheckAuth(w, r, "job-log"); !ok {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body")
			return
		}
		r.Body.Close()
		var params protocol.JobLogParams
		if err := json.Unmarshal(body, &params); err != nil || params.JobID == "" {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "job_id is required")
			return
		}
		resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeJobLog, params, 15*time.Second, "")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
			return
		}
		// P8-07: the job log is one of the four sinks; it passes through the
		// same redactor as the response body.
		writeJSON(w, http.StatusOK, s.redactValue(resp))
	}
}

// handleV1AgentJobs lists the witness records for one agent.
func (s *Server) handleV1AgentJobs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		if _, ok := s.v1CheckAuth(w, r, "job-status"); !ok {
			return
		}
		s.jobMu.Lock()
		out := make([]ServerJob, 0, len(s.jobs))
		for _, j := range s.jobs {
			if j.AgentID == agentID {
				out = append(out, *j)
			}
		}
		s.jobMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": out, "count": len(out)})
	}
}
