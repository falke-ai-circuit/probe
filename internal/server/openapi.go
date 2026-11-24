package server

import (
	"encoding/json"
	"net/http"
)

// ---------------------------------------------------------------------------
// OpenAPI 3.0 specification — hand-written, served at /openapi.json
// ---------------------------------------------------------------------------

// openapiSpec is the OpenAPI 3.0 document for the PROBE REST API v1. It is
// constructed as a Go map and marshalled to JSON on each request. The spec
// covers all /api/v1/ endpoints plus the /openapi.json endpoint itself.
func openapiSpec() map[string]interface{} {
	// Common response schema references.
	apiResponseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ok": map[string]interface{}{
				"type":        "boolean",
				"description": "True for success, false for error",
			},
			"data": map[string]interface{}{
				"description": "Response data (present when ok=true)",
			},
			"error": map[string]interface{}{
				"$ref": "#/components/schemas/APIError",
			},
		},
		"required": []string{"ok"},
	}

	apiErrorSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Machine-readable error code",
				"example":     "AGENT_UNREACHABLE",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Human-readable error message",
				"example":     "agent vegas-c2022 not connected",
			},
		},
		"required": []string{"code", "message"},
	}

	agentRecordSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent_id":       map[string]interface{}{"type": "string"},
			"name":           map[string]interface{}{"type": "string"},
			"version":        map[string]interface{}{"type": "string"},
			"os":             map[string]interface{}{"type": "string"},
			"arch":           map[string]interface{}{"type": "string"},
			"mode":           map[string]interface{}{"type": "string"},
			"connected_at":   map[string]interface{}{"type": "string", "format": "date-time"},
			"last_heartbeat": map[string]interface{}{"type": "string", "format": "date-time"},
			"status":         map[string]interface{}{"type": "string", "enum": []string{"active", "inactive", "stale", "error"}},
			"uptime_seconds":  map[string]interface{}{"type": "integer"},
			"error_count":    map[string]interface{}{"type": "integer"},
			"health_score":   map[string]interface{}{"type": "number"},
			// P12-03 — agent-state visibility. `stale` is the one field a
			// caller needs in order to know the agent is not serving; it is
			// derived from last_seen against stale_threshold_seconds and,
			// when the transport the exec path uses is gone, forced true.
			"last_seen":                 map[string]interface{}{"type": "string", "format": "date-time"},
			"last_seen_age_seconds":     map[string]interface{}{"type": "integer"},
			"stale":                     map[string]interface{}{"type": "boolean"},
			"stale_reason":              map[string]interface{}{"type": "string", "enum": []string{"ok", "no_transport", "inactive", "error", "never_seen", "heartbeat_age", "ping_unanswered"}, "description": "why stale is true; ok when it is false"},
			"stale_threshold_seconds":   map[string]interface{}{"type": "integer", "description": "threshold stale was derived from (90s)"},
			"reconnects":                map[string]interface{}{"type": "integer", "description": "monotonic count of re-registrations"},
			"connection_started_at":     map[string]interface{}{"type": "string", "format": "date-time"},
			"connection_uptime_seconds": map[string]interface{}{"type": "integer", "description": "age of the current connection"},
			"execution_context":         map[string]interface{}{"type": "string", "description": "contract selector id the agent serves (P10-07 v3.1); agent_own until the agent advertises a concrete context"},
			"ping_timeouts":             map[string]interface{}{"type": "integer", "description": "monotonic count of pings the agent did not answer"},
			"ping_misses":               map[string]interface{}{"type": "integer", "description": "current consecutive unanswered pings"},
			"last_ping_timeout_at":      map[string]interface{}{"type": "string", "format": "date-time"},
		},
	}

	operatorSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":         map[string]interface{}{"type": "string"},
			"name":       map[string]interface{}{"type": "string"},
			"role":       map[string]interface{}{"type": "string", "enum": []string{"admin", "operator", "viewer"}},
			"created_at": map[string]interface{}{"type": "string", "format": "date-time"},
			"last_seen":  map[string]interface{}{"type": "string", "format": "date-time"},
		},
	}

	execParamsSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string"},
			"timeout": map[string]interface{}{"type": "integer", "description": "seconds, default 60"},
			"workdir": map[string]interface{}{"type": "string"},
			"env":     map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"command"},
	}

	fsParamsSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":   map[string]interface{}{"type": "string"},
			"offset": map[string]interface{}{"type": "integer"},
			"limit":  map[string]interface{}{"type": "integer"},
			"data":   map[string]interface{}{"type": "string", "description": "base64 for writes"},
			"mode":   map[string]interface{}{"type": "string", "description": "octal string for writes"},
			"from":   map[string]interface{}{"type": "string", "description": "for moves"},
			"to":     map[string]interface{}{"type": "string", "description": "for moves"},
		},
	}

	tunnelParamsSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target_host":  map[string]interface{}{"type": "string"},
			"target_port":  map[string]interface{}{"type": "integer"},
			"listen_port":  map[string]interface{}{"type": "integer", "description": "0 = auto"},
		},
		"required": []string{"target_host", "target_port"},
	}

	mitmStartSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"listen_addr": map[string]interface{}{"type": "string"},
			"target_addr": map[string]interface{}{"type": "string"},
			"log_path":    map[string]interface{}{"type": "string"},
			"reuse_addr":  map[string]interface{}{"type": "boolean"},
		},
	}

	debugAttachSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pid":          map[string]interface{}{"type": "integer"},
			"process_name": map[string]interface{}{"type": "string"},
		},
	}

	debugReadMemSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"debug_id": map[string]interface{}{"type": "string"},
			"address":  map[string]interface{}{"type": "integer"},
			"size":     map[string]interface{}{"type": "integer"},
		},
		"required": []string{"debug_id", "address", "size"},
	}

	updateParamsSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"binary_path":   map[string]interface{}{"type": "string"},
			"version":       map[string]interface{}{"type": "string"},
			"download_host": map[string]interface{}{"type": "string"},
		},
		"required": []string{"binary_path"},
	}

	createOperatorSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"role":  map[string]interface{}{"type": "string", "enum": []string{"admin", "operator", "viewer"}},
			"token": map[string]interface{}{"type": "string", "description": "optional; auto-generated if empty"},
		},
		"required": []string{"name", "role"},
	}

	// Helper to build a standard 200 response.
	okResponse := func(desc string) map[string]interface{} {
		return map[string]interface{}{
			"description": desc,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{"$ref": "#/components/schemas/APIResponse"},
				},
			},
		}
	}

	// Helper to build an error response.
	errorResponse := func(code, desc string) map[string]interface{} {
		return map[string]interface{}{
			"description": desc,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": map[string]interface{}{"$ref": "#/components/schemas/APIResponse"},
				},
			},
		}
	}

	// Helper to build a request body.
	requestBody := func(schema map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"required": true,
			"content": map[string]interface{}{
				"application/json": map[string]interface{}{
					"schema": schema,
				},
			},
		}
	}

	// Helper: agent-id path parameter.
	agentIDParam := func() map[string]interface{} {
		return map[string]interface{}{
			"name":        "id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]interface{}{"type": "string"},
			"description": "Agent ID",
		}
	}

	// Helper: operator-id path parameter.
	operatorIDParam := func() map[string]interface{} {
		return map[string]interface{}{
			"name":        "id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]interface{}{"type": "string"},
			"description": "Operator ID",
		}
	}

	// Common error responses for agent endpoints.
	agentErrors := map[string]interface{}{
		"400": errorResponse("BAD_REQUEST", "Invalid request body"),
		"403": errorResponse("FORBIDDEN", "Permission denied"),
		"404": errorResponse("NOT_FOUND", "Agent not found"),
		"503": errorResponse("SERVICE_UNAVAILABLE", "Agent unreachable or error"),
		"504": errorResponse("TIMEOUT", "Agent did not respond in time"),
	}

	// Build the paths map.
	paths := map[string]interface{}{}

	// Server health
	paths["/api/v1/health"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "Server health",
			"description": "Returns server uptime and agent statistics.",
			"responses": map[string]interface{}{
				"200": okResponse("Server health info"),
			},
		},
	}

	// List agents
	paths["/api/v1/agents"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "List agents",
			"description": "Returns all registered agents.",
			"responses": map[string]interface{}{
				"200": okResponse("List of agent records"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
	}

	// Agent by ID
	paths["/api/v1/agents/{id}"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "Get agent details",
			"description": "Returns a single agent's full record.",
			"parameters":  []interface{}{agentIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Agent record"),
				"404": errorResponse("NOT_FOUND", "Agent not found"),
			},
		},
		"delete": map[string]interface{}{
			"summary":     "Remove agent",
			"description": "Unregisters an agent from the registry.",
			"parameters":  []interface{}{agentIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Agent removed"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
	}

	// Agent health
	paths["/api/v1/agents/{id}/health"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":    "Get agent health",
			"description": "Returns the health record for a single agent.",
			"parameters": []interface{}{agentIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Agent health record"),
				"404": errorResponse("NOT_FOUND", "Agent not found"),
			},
		},
	}

	// Agent audit
	paths["/api/v1/agents/{id}/audit"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":    "Get agent audit log",
			"description": "Returns audit entries for a specific agent.",
			"parameters": []interface{}{agentIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Audit entries"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
	}

	// Agent command endpoints — all POST, all take agent ID path param.
	agentCommandPaths := map[string]map[string]interface{}{
		"/api/v1/agents/{id}/exec": {
			"summary":  "Execute command",
			"params":   execParamsSchema,
		},
		"/api/v1/agents/{id}/fs-list": {
			"summary":  "List directory",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-read": {
			"summary":  "Read file",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-write": {
			"summary":  "Write file",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-stat": {
			"summary":  "Stat file",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-hash": {
			"summary":  "Hash file (SHA256)",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-move": {
			"summary":  "Move file",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-mkdir": {
			"summary":  "Make directory",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/fs-delete": {
			"summary":  "Delete file",
			"params":   fsParamsSchema,
		},
		"/api/v1/agents/{id}/proc-list": {
			"summary":  "List processes",
			"params":   nil,
		},
		"/api/v1/agents/{id}/proc-kill": {
			"summary":  "Kill process",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"pid": map[string]interface{}{"type": "integer"}, "signal": map[string]interface{}{"type": "integer"}}},
		},
		"/api/v1/agents/{id}/proc-start": {
			"summary":  "Start process",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}, "workdir": map[string]interface{}{"type": "string"}, "env": map[string]interface{}{"type": "object"}, "background": map[string]interface{}{"type": "boolean"}}},
		},
		"/api/v1/agents/{id}/capture": {
			"summary":  "Screen capture",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"display": map[string]interface{}{"type": "integer"}, "quality": map[string]interface{}{"type": "integer"}}},
		},
		"/api/v1/agents/{id}/tunnel": {
			"summary":  "Open tunnel",
			"params":   tunnelParamsSchema,
		},
		"/api/v1/agents/{id}/tunnel-close": {
			"summary":  "Close tunnel",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"tunnel_id": map[string]interface{}{"type": "string"}}},
		},
		"/api/v1/agents/{id}/mitm-start": {
			"summary":  "Start MITM proxy",
			"params":   mitmStartSchema,
		},
		"/api/v1/agents/{id}/mitm-stop": {
			"summary":  "Stop MITM proxy",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"mitm_id": map[string]interface{}{"type": "string"}}},
		},
		"/api/v1/agents/{id}/mitm-traffic": {
			"summary":  "Get MITM traffic",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"mitm_id": map[string]interface{}{"type": "string"}}},
		},
		"/api/v1/agents/{id}/debug-attach": {
			"summary":  "Attach debugger",
			"params":   debugAttachSchema,
		},
		"/api/v1/agents/{id}/debug-detach": {
			"summary":  "Detach debugger",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"debug_id": map[string]interface{}{"type": "string"}}},
		},
		"/api/v1/agents/{id}/debug-read-mem": {
			"summary":  "Read memory",
			"params":   debugReadMemSchema,
		},
		"/api/v1/agents/{id}/debug-modules": {
			"summary":  "List modules",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"debug_id": map[string]interface{}{"type": "string"}}},
		},
		"/api/v1/agents/{id}/debug-mem-query": {
			"summary":  "Query memory region",
			"params":   map[string]interface{}{"type": "object", "properties": map[string]interface{}{"debug_id": map[string]interface{}{"type": "string"}, "address": map[string]interface{}{"type": "integer"}}},
		},
		"/api/v1/agents/{id}/update": {
			"summary":  "Push agent update",
			"params":   updateParamsSchema,
		},
	}

	for path, meta := range agentCommandPaths {
		operation := map[string]interface{}{
			"summary":     meta["summary"],
			"description": meta["summary"].(string) + " on the remote agent via WebSocket.",
			"parameters":  []interface{}{agentIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Command result"),
			},
		}
		// Merge common agent errors.
		for k, v := range agentErrors {
			operation["responses"].(map[string]interface{})[k] = v
		}
		// Add request body if the endpoint takes params.
		if params, ok := meta["params"]; ok && params != nil {
			operation["requestBody"] = requestBody(params.(map[string]interface{}))
		}
		paths[path] = map[string]interface{}{
			"post": operation,
		}
	}

	// Audit query
	paths["/api/v1/audit"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "Query audit log",
			"description": "Queries the audit log with optional filters.",
			"parameters": []interface{}{
				map[string]interface{}{"name": "agent_id", "in": "query", "schema": map[string]interface{}{"type": "string"}},
				map[string]interface{}{"name": "operator_id", "in": "query", "schema": map[string]interface{}{"type": "string"}},
				map[string]interface{}{"name": "action", "in": "query", "schema": map[string]interface{}{"type": "string"}},
				map[string]interface{}{"name": "limit", "in": "query", "schema": map[string]interface{}{"type": "integer"}},
				map[string]interface{}{"name": "from", "in": "query", "schema": map[string]interface{}{"type": "string", "format": "date-time"}},
				map[string]interface{}{"name": "to", "in": "query", "schema": map[string]interface{}{"type": "string", "format": "date-time"}},
			},
			"responses": map[string]interface{}{
				"200": okResponse("Audit entries"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
	}

	// Operators
	paths["/api/v1/operators"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "List operators",
			"description": "Returns all configured operators.",
			"responses": map[string]interface{}{
				"200": okResponse("List of operators"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
		"post": map[string]interface{}{
			"summary":     "Create operator",
			"description": "Creates a new operator with the given name, role, and optional token.",
			"requestBody": requestBody(createOperatorSchema),
			"responses": map[string]interface{}{
				"201": okResponse("Created operator"),
				"400": errorResponse("BAD_REQUEST", "Invalid params"),
				"403": errorResponse("FORBIDDEN", "Permission denied"),
			},
		},
	}

	paths["/api/v1/operators/{id}"] = map[string]interface{}{
		"delete": map[string]interface{}{
			"summary":     "Delete operator",
			"description": "Removes an operator by ID.",
			"parameters":  []interface{}{operatorIDParam()},
			"responses": map[string]interface{}{
				"200": okResponse("Operator deleted"),
				"404": errorResponse("NOT_FOUND", "Operator not found"),
			},
		},
	}

	// OpenAPI spec endpoint itself.
	paths["/openapi.json"] = map[string]interface{}{
		"get": map[string]interface{}{
			"summary":     "OpenAPI specification",
			"description": "Returns this OpenAPI 3.0 specification as JSON.",
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "OpenAPI 3.0 JSON document",
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"type": "object"},
						},
					},
				},
			},
		},
	}

	// Login endpoint
	loginSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"username": map[string]interface{}{"type": "string"},
			"password": map[string]interface{}{"type": "string", "format": "password"},
		},
		"required": []string{"username", "password"},
	}
	paths["/api/v1/login"] = map[string]interface{}{
		"post": map[string]interface{}{
			"summary":     "Login",
			"description": "Authenticates an operator with username/password and returns their API token.",
			"requestBody": requestBody(loginSchema),
			"responses": map[string]interface{}{
				"200": okResponse("Login successful — returns token and operator"),
				"401": errorResponse("UNAUTHORIZED", "Invalid credentials"),
			},
		},
	}

	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "PROBE REST API",
			"description": "Platform for Remote Operations & Bridge Environment — REST API v1",
			"version":     "1.0.0",
		},
		"servers": []interface{}{
			map[string]interface{}{"url": "/api/v1", "description": "v1 API base path"},
		},
		"paths": paths,
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{
				"APIResponse":   apiResponseSchema,
				"APIError":      apiErrorSchema,
				"AgentRecord":   agentRecordSchema,
				"Operator":      operatorSchema,
				"ExecParams":     execParamsSchema,
				"FSParams":      fsParamsSchema,
				"TunnelParams":  tunnelParamsSchema,
				"MitmStartParams": mitmStartSchema,
				"DebugAttachParams": debugAttachSchema,
				"DebugReadMemParams": debugReadMemSchema,
				"AgentUpdateParams": updateParamsSchema,
				"CreateOperatorParams": createOperatorSchema,
			},
		},
	}
}

// handleOpenAPI serves the OpenAPI 3.0 specification at /openapi.json.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(openapiSpec())
}