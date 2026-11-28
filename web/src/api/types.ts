// API types matching the Go backend structures

export interface APIResponse<T> {
  ok: boolean
  data?: T
  error?: { code: string; message: string }
}

export interface AgentRecord {
  agent_id: string
  name: string
  version: string
  os: string
  arch: string
  mode: string
  capabilities?: string[]
  connected_at: string
  last_heartbeat: string
  status: string // active, inactive, stale, error
  uptime_seconds: number
  last_error?: string
  error_count: number
  health_score: number
  resource_usage?: {
    cpu_percent: number
    memory_mb: number
    disk_free_mb: number
  }
  // P23-09 provenance (P18-02): names the relay carrying this agent's
  // transport, derived server-side on every read. Empty/absent means the
  // agent is connected directly — this is the same field P18-01's
  // Network.tsx consumes via the /topology via/origin chain.
  via?: string
}

export interface HealthInfo {
  status: string
  server_version?: string
  total_agents: number
  active_agents: number
  stale_agents: number
  uptime_seconds: number
  // P27-03 (spec P27-01 §2.1): the server's E2E posture, read from the
  // manager that decides every refusal. `active` is a real bool; cipher_error
  // non-empty means the key build failed and the server accepts nothing — the
  // UI must surface it rather than render posture-ok. Absent => unknown.
  e2e?: { active: boolean; cipher_error: string } | null
  // P27-03 (spec P27-01 §5.3 rule 6): the uplink roll-up. A glance-level "ok"
  // must never print while an engaged uplink carries nothing, so the dashboard
  // binds its chip to carrying < connected, not to `status`.
  uplinks_total?: number
  uplinks_connected?: number
  uplinks_carrying?: number
  // P22-02 §3.1: four-key identity block, published UNAUTHENTICATED. Null
  // when PROBE_PREAUTH_IDENTITY=off — renders UNRESOLVED, never a guess.
  identity?: {
    name: string
    listen_addr: string
    version?: string | null
    role: string
  } | null
}

export interface BuildConfig {
  id?: string
  name: string
  os: string
  arch: string
  capabilities: string[]
  server_url: string
  token: string
  permissions: string
  sandbox_dir?: string
  disguise?: DisguiseConfig
  autostart: boolean
  backoff_min?: string
  backoff_max?: string
  max_retries?: number
  status?: string
  binary_path?: string
  created_at?: string
  completed_at?: string
  error?: string
  vt_status?: string      // pending, scanning, clean, dirty
  vt_detections?: number
  vt_report_url?: string
}

export interface DisguiseConfig {
  enabled: boolean
  filename: string
  company: string
  description: string
  product_name: string
}

export interface Profile {
  id: string
  name: string
  os: string
  arch: string
  capabilities: string[]
  server_url: string
  permissions: string
  sandbox_dir?: string
  disguise?: DisguiseConfig
  autostart: boolean
  backoff_min?: string
  backoff_max?: string
  max_retries?: number
  created_at: string
}

export interface Task {
  id: string
  agent_id: string
  command_type: string
  params: unknown
  schedule: {
    type: string
    delay_seconds?: number
    interval_seconds?: number
    max_retries?: number
    retry_count?: number
  }
  status: string
  result?: unknown
  error?: string
  created_at: string
  execute_at: string
  started_at?: string
  completed_at?: string
  operator_id?: string
}

export interface Operator {
  id: string
  name: string
  role: string
  created_at: string
  last_seen?: string
}

export interface EnrollmentToken {
  token: string
  agent_name: string
  created_at: string
  expires_at: string
  used: boolean
}

export interface AuditEntry {
  id: string
  timestamp: string
  agent_id: string
  operator_id: string
  action: string
  command_type?: string
  result?: string
  error?: string
}

export interface RevokedAgent {
  agent_id: string
  revoked_at: string
  reason: string
}

export interface FileTransfer {
  id: string
  agent_id: string
  direction: string      // "upload" or "download"
  remote_path: string
  total_size: number
  offset: number
  chunk_size: number
  sha256?: string
  status: string         // "pending", "transferring", "completed", "failed", "paused"
  created_at: string
  updated_at: string
  error?: string
}

export interface CredentialMatch {
  type: string            // "password", "hash", "api_key", "token", "connection_string"
  source: string          // agent ID or "manual"
  context: string         // surrounding text
  value: string           // the matched credential
  timestamp: string
}

export interface SecurityStatus {
  ip_filter_active: boolean
  allowed_cidr?: string
  blacklist_active: boolean
  blacklist_count: number
  require_api_auth: boolean
  operators_count: number
  audit_log_active: boolean
  tls: boolean
  mtls: boolean
  token_ttl?: string
  login_rate_limit: LoginAttemptsStatus
}

export interface LoginAttemptsStatus {
  tracked_ips: number
  locked_ips: number
  max_failures: number
  window_seconds: number
  lock_seconds: number
}

// P18-04 (§11.4) — read-only effective network/security toggles.
// Mirrors internal/server settingsPayload; carries paths and booleans only,
// never cert/key/token material (P17-03).
export interface SettingsStatus {
  e2e: { enabled: boolean; state: string }
  tls: {
    enabled: boolean
    cert_path: string
    key_path: string
    client_ca_path: string
    ca_configured: boolean
    mtls_active: boolean
    state: string
  }
  token_rotation: { ttl_seconds: number; ttl: string; active: boolean; guard: string }
  relay_downstream: {
    policy: string
    tokens_required: boolean
    allow_any_downstream: boolean
    allowlist: string[]
  }
}

// Flow runtime (v1.13.0) — types mirror the Go Flow/Step/Run structs.

export interface FlowStep {
  id: string
  type: string
  next?: string
  on_error?: string
  command_type?: string
  params?: Record<string, unknown>
  as?: string
  store_as?: string
  seconds?: number
  condition?: string
  if_true?: string
  if_false?: string
  left?: string
  right?: string
  diff_as?: string
  input?: string
  rules?: Array<{ if: string; label: string }>
  classify_as?: string
  signal?: string
  payload?: unknown
  body?: FlowStep[]
  interval_seconds?: number
  max_iterations?: number
  stop_condition?: string
}

export interface FlowTrigger {
  type: string
  delay_seconds?: number
  interval_seconds?: number
}

export interface FlowRecord {
  id: string
  name: string
  description?: string
  enabled: boolean
  trigger: FlowTrigger
  steps: FlowStep[]
  agent_ids?: string[]
  created_at: string
  updated_at: string
  created_by?: string
}

export interface FlowRun {
  id: string
  flow_id: string
  agent_id?: string
  status: string
  started_at: string
  completed_at?: string
  duration_ms?: number
  error?: string
  state?: Record<string, unknown>
  node_status?: Record<string, string>
  active_node?: string
}

export interface FlowTemplate {
  name: string
  description?: string
  trigger: FlowTrigger
  steps: FlowStep[]
}

export interface SurveyEvent {
  id: string
  flow_id: string
  run_id: string
  agent_id?: string
  signal: string
  payload: unknown
  timestamp: string
}

// Sensor subsystem (v1.14.0)

export interface SensorInfo {
  name: string
  category: string
  description: string
  args?: string[]
}

export interface SensorState {
  enabled: boolean
  args?: Record<string, string>
}

export interface SensorAssignment {
  agent_id: string
  sensors: Record<string, SensorState>
}

// Peers (P17-02): the API projection of a configured default_peer.
// §6: token_present + token_fp only — the secret never crosses the wire.
export interface LinkStateDetail {
  peer_version?: string
  required_min?: string
  refusal_code?: string
  refusal_reason?: string
  token_unsealable?: boolean
}

export interface PeerConfigView {
  id: string
  upstream: string
  token_fp: string
  token_present: boolean
  enabled: boolean
  // P25-05R: this peer is wired as the server's forward uplink (forward target).
  uplink?: boolean
  // P27-02 (spec §3.2): the same three keys the stored record carries, echoed
  // on the POST read-back and on every list row.
  uplink_type?: UplinkType
  schedule_start?: string
  schedule_end?: string
  listen: string
  max_agents: number
  created_at: string
  updated_at: string
  // '' | connected | retrying | auth-rejected | version-mismatch | disabled | unreachable
  link_state: string
  state_detail: LinkStateDetail
}

// P18-06: server inventory (GET /api/v1/servers, P21-02/P22-02) — mirrors the
// Go serverEntry/serverCounts servers.go projection. Every key is always
// emitted by the server; an unknown value is null.
export interface ServerCounts {
  direct_agents: number
  relayed_agents: number
}

export interface ServerTransport {
  kind: string
  url?: string | null
  port?: number | null
  ws_state: string
  connected_at?: string | null
  source: string
}

// P27-03 (spec P27-01 §1.1): the six-value state enum. `disabled` and
// `unreachable` were added in P27-02; `up`/`down` are gone from the wire. Any
// other string is UNMAPPED and must render as an unknown chip (C3), never as
// healthy — see servers-view.ts stateChip().
export type ServerState =
  | 'connected'
  | 'retrying'
  | 'auth-rejected'
  | 'version-mismatch'
  | 'disabled'
  | 'unreachable'

export type UplinkType = 'always-on' | 'scheduled' | 'manual'

// §1.1 schedule_window: non-null ONLY when uplink_type=scheduled (server-local
// HH:MM, start>end wraps midnight). null is the honest absence for the other
// two disciplines — the UI renders "—", never a fabricated window.
export interface ScheduleWindow {
  start: string
  end: string
}

// §2.3 server-LINK encryption truth. `payload` is "none" for server rows: the
// relay/uplink control plane is TLS-only and never E2E-sealed. Both values are
// read from the row; an absent transport renders as unknown, never as green.
export interface ServerEncrypted {
  transport: string // wss | ws | "" (unknown / scheme-less)
  payload: string   // always "none" for server rows
}

// §5.2 connected-but-not-carrying counters.
export interface CarryingRefusal {
  code: string
  reason: string
  at: string
}

export interface ServerCarrying {
  channels_open: number
  agents_registered: number
  last_refusal: CarryingRefusal | null
}

export interface ServerEntry {
  id: string
  name: string
  role: string
  listen_addr: string
  listen_addr_raw: string
  listen_addr_source: string
  version?: string | null
  uptime_seconds?: number | null
  state: string
  last_seen: string
  state_reason: string
  down_since?: string | null
  transports: ServerTransport[]
  counts: ServerCounts
  forward_policy?: string | null
  max_agents?: number | null
  // P27-03 (spec P27-01 §1.1) row-model additions — every key emitted on
  // every row by the server.
  address: string
  direction: string // uplink | inbound
  uplink_type: UplinkType | null // null on bootstrap rows: honest absence
  schedule_window: ScheduleWindow | null
  in_window: boolean
  enabled: boolean
  peer_version: string | null
  required_min: string | null
  encrypted: ServerEncrypted
  carrying: ServerCarrying
  source: string // local | peers | bootstrap
}

export interface ServersResponse {
  generated_at: string
  local_id: string
  retention_seconds: number
  servers: ServerEntry[]
}

// P27-03 (spec P27-01 §5.3 rule 4): GET /api/v1/chain. The server-role hop
// carries `registration_state` (registered | refused | unanswered) and is
// OMITTED where no uplink exists. A `refused` hop folds the chain to
// `degraded` with failing_hop named — the UI renders that, never "healthy".
export interface ChainHop {
  id: string
  role: string // server | relay | agent
  state: string
  error?: string
  channels?: number
  observed: boolean
  last_seen?: string
  registration_state?: string
}

export interface ChainFailing {
  id: string
  error?: string
}

export interface ChainView {
  id: string
  state: string // healthy | degraded | down
  path: string[]
  hops: ChainHop[]
  failing_hop?: ChainFailing
  agent?: string
  degraded_reason?: string
  observed_at: string
  observe_bound_ms: number
}

export interface ChainResponse {
  chains: ChainView[]
  count: number
  observe_bound_ms: number
}
