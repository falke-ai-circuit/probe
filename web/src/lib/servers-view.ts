// P27-03 (spec P27-01) — pure view logic for the Servers surface.
//
// Every function here is a BINDING, not a decoration: each one takes a value the
// server actually emits and returns what the UI may claim about it. Nothing is
// hardcoded to a positive result. Two rules drive the whole module:
//
//   C3  graceful-unknown — an absent, unmeasured or unrecognised value renders
//       as unknown (grey), never as healthy. `stateChip` has no default-green.
//   §5.3 never-green-while-dead — a link that is up but not carrying, or a
//       chain whose registration was refused, is NEVER healthy. The only green
//       `carryingVerdict` can return is `connected` AND `agents_registered > 0`.
//
// Keeping this out of the .tsx files is deliberate: the acceptance probes (B-2,
// B-3, never-green) can then flip an API value in a test and assert the
// rendering follows, with no DOM involved.

import type {
  ServerEntry, ServerEncrypted, ServerCarrying, ChainView, UplinkType,
} from '../api/types'

// ---------------------------------------------------------------------------
// Chips: one shape for every badge in the surface.
// ---------------------------------------------------------------------------

export interface Chip {
  /** Text rendered inside the chip. */
  label: string
  /** Full className (colour + per-state visual mark). */
  className: string
  /** false when the value was absent or outside the known vocabulary. */
  known: boolean
  /** true = the state never auto-recovers without an operator (spec §1.2). */
  terminal: boolean
}

// The six §1.1 states, in the server's own words. `up`/`down` are deliberately
// ABSENT: they were migrated away in P27-02, so a row still saying "up" is
// unmapped and must fall through to the unknown chip rather than inherit green.
const STATE_CHIPS: Record<string, Chip> = {
  connected: { label: 'connected', className: 'badge badge-green state-connected', known: true, terminal: false },
  retrying: { label: 'retrying', className: 'badge badge-yellow state-retrying', known: true, terminal: false },
  'auth-rejected': { label: 'auth-rejected', className: 'badge badge-red state-auth-rejected', known: true, terminal: true },
  'version-mismatch': { label: 'version-mismatch', className: 'badge badge-red state-version-mismatch', known: true, terminal: true },
  disabled: { label: 'disabled', className: 'badge badge-gray state-disabled', known: true, terminal: false },
  unreachable: { label: 'unreachable', className: 'badge badge-red state-unreachable', known: true, terminal: false },
}

/** The state vocabulary this view knows how to render. */
export const SERVER_STATES = Object.keys(STATE_CHIPS)

/**
 * stateChip maps one wire `state` onto a chip. Any value outside the six is an
 * UNKNOWN chip — grey, `known:false`, and the raw wire word is preserved in the
 * label so the operator can see what the server actually said.
 */
export function stateChip(state: string | null | undefined): Chip {
  const s = (state ?? '').trim()
  const hit = STATE_CHIPS[s]
  if (hit) return hit
  return {
    label: s ? `unknown (${s})` : 'unknown',
    className: 'badge badge-gray state-unknown',
    known: false,
    terminal: false,
  }
}

/**
 * directionChip names which side dials (spec §1.1 `direction`). A server row is
 * never mistakable for an agent row: agents have no direction, servers do.
 */
export function directionChip(direction: string | null | undefined): Chip {
  switch ((direction ?? '').trim()) {
    case 'uplink':
      return { label: '→ we dial', className: 'badge badge-blue state-direction-uplink', known: true, terminal: false }
    case 'inbound':
      return { label: '← it dials us', className: 'badge badge-blue state-direction-inbound', known: true, terminal: false }
    default:
      return { label: 'direction unknown', className: 'badge badge-gray state-unknown', known: false, terminal: false }
  }
}

/**
 * uplinkTypeLabel renders `uplink_type` + `schedule_window`. A null type is a
 * bootstrap row and renders an em dash — spec §1.1: honest absence beats a
 * fake "always-on". A window on a non-scheduled row is not invented here.
 */
export function uplinkTypeLabel(t: UplinkType | string | null | undefined, win?: { start: string; end: string } | null, inWindow?: boolean): string {
  switch ((t ?? '').trim()) {
    case 'always-on':
      return 'always-on'
    case 'manual':
      return 'manual'
    case 'scheduled': {
      if (!win || !win.start || !win.end) return 'scheduled (window absent)'
      const span = `${win.start}–${win.end}`
      if (inWindow === false) return `scheduled ${span} (outside window)`
      return `scheduled ${span}`
    }
    case '':
      return '—'
    default:
      return `unknown (${t})`
  }
}

// ---------------------------------------------------------------------------
// §2.3 encryption badge — bound to the row's own encrypted object.
// ---------------------------------------------------------------------------

/**
 * encryptionBadge renders the SERVER-LINK transport truth. It is not the agent
 * E2E badge and never inherits it: wss = TLS (green), ws = cleartext (red),
 * anything else — including a missing object — = UNKNOWN (grey). There is no
 * branch that returns green without `transport === "wss"` on the wire.
 */
export function encryptionBadge(enc?: ServerEncrypted | null): Chip {
  const t = (enc?.transport ?? '').trim().toLowerCase()
  if (t === 'wss') {
    return { label: 'TLS · wss', className: 'badge badge-green state-encrypted-tls', known: true, terminal: false }
  }
  if (t === 'ws') {
    return { label: 'CLEARTEXT · ws', className: 'badge badge-red state-encrypted-clear', known: true, terminal: false }
  }
  return { label: 'ENCRYPTION UNKNOWN', className: 'badge badge-gray state-unknown', known: false, terminal: false }
}

/**
 * payloadLabel renders `encrypted.payload` honestly. "none" is the only value
 * the spec permits today and it means the control plane is TLS-only — which is
 * NOT the same as E2E-sealed, so it is stated, never shown as a second green.
 * An absent value renders unknown; anything else is quoted verbatim.
 */
export function payloadLabel(enc?: ServerEncrypted | null): string {
  const p = (enc?.payload ?? '').trim()
  if (p === '') return 'payload encryption unknown'
  if (p === 'none') return 'payload: none (control plane is TLS-only, not E2E)'
  return `payload: ${p}`
}

/**
 * e2ePostureChip renders the server-wide posture from /health. It is shown
 * beside the servers table because a server row's TLS badge says nothing about
 * whether this process accepts sealed agents; and cipher_error means the server
 * accepts NOTHING, so it can never render as on.
 */
export function e2ePostureChip(e2e?: { active: boolean; cipher_error: string } | null): Chip {
  if (!e2e) return { label: 'e2e posture unknown', className: 'badge badge-gray state-unknown', known: false, terminal: false }
  if (e2e.cipher_error) {
    return { label: `e2e CIPHER ERROR — refusing all`, className: 'badge badge-red state-e2e-error', known: true, terminal: false }
  }
  if (e2e.active) return { label: 'e2e posture: on', className: 'badge badge-green state-e2e-on', known: true, terminal: false }
  return { label: 'e2e posture: off', className: 'badge badge-gray state-e2e-off', known: true, terminal: false }
}

// ---------------------------------------------------------------------------
// §5.3 carrying truth — the never-green rule.
// ---------------------------------------------------------------------------

export type CarryingLevel = 'healthy' | 'idle' | 'impaired' | 'not-connected'

export interface CarryingVerdict {
  level: CarryingLevel
  chip: Chip
  /** One-line operator-readable explanation; always states the evidence. */
  detail: string
}

/**
 * carryingVerdict applies spec §5.3 rules 1–3 to one row.
 *
 *   healthy       connected AND agents_registered > 0 — the ONLY green.
 *   impaired      connected AND 0 registered AND evidence forwarding was
 *                 attempted: an upstream refusal on record, or channels opened
 *                 that nothing confirmed.
 *   idle          connected AND 0 registered AND no attempt observed.
 *   not-connected any other state — carrying is not a question you can answer.
 *
 * FINDING (carried in the artefact): the row does not expose an explicit
 * "forwarding engaged" boolean, so idle-vs-impaired is derived from
 * `carrying.channels_open` + `carrying.last_refusal`. Both non-healthy branches
 * are amber/red, so the derivation can never manufacture a green.
 */
export function carryingVerdict(row: Pick<ServerEntry, 'state' | 'carrying' | 'enabled'>): CarryingVerdict {
  const c: ServerCarrying = row.carrying ?? { channels_open: 0, agents_registered: 0, last_refusal: null }
  const state = (row.state ?? '').trim()

  if (state !== 'connected') {
    return {
      level: 'not-connected',
      chip: { label: `carrying: n/a (${state || 'unknown'})`, className: 'badge badge-gray state-unknown', known: state !== '', terminal: false },
      detail: `link is ${state || 'unknown'} — carrying cannot be claimed while the link is not up`,
    }
  }
  if (c.agents_registered > 0) {
    return {
      level: 'healthy',
      chip: { label: `carrying ${c.agents_registered}`, className: 'badge badge-green state-carrying', known: true, terminal: false },
      detail: `${c.agents_registered} agent(s) registered over this link; ${c.channels_open} channel(s) open`,
    }
  }
  if (c.last_refusal) {
    return {
      level: 'impaired',
      chip: { label: `NOT carrying — refused ${c.last_refusal.code}`, className: 'badge badge-red state-impaired', known: true, terminal: false },
      detail: `connected but NOT carrying: upstream refused (${c.last_refusal.code}) at ${c.last_refusal.at}: ${c.last_refusal.reason}`,
    }
  }
  if (c.channels_open > 0) {
    return {
      level: 'impaired',
      chip: { label: `NOT carrying — ${c.channels_open} channel(s) unconfirmed`, className: 'badge badge-red state-impaired', known: true, terminal: false },
      detail: `connected but NOT carrying: ${c.channels_open} channel(s) opened upstream and none registered`,
    }
  }
  return {
    level: 'idle',
    chip: { label: 'connected, carrying none', className: 'badge badge-yellow state-idle', known: true, terminal: false },
    detail: 'connected and carrying none — no forwarding attempt observed on this link',
  }
}

/**
 * chainVerdict applies §5.3 rule 4 to one chain. A server hop whose
 * `registration_state` is `refused` never renders healthy — even if the chain
 * object still says so — and `unanswered` is amber, not green.
 */
export function chainVerdict(chain: Pick<ChainView, 'state' | 'hops' | 'failing_hop' | 'degraded_reason'>): Chip {
  const serverHop = (chain.hops ?? []).find(h => h.role === 'server')
  const reg = (serverHop?.registration_state ?? '').trim()
  const state = (chain.state ?? '').trim()

  if (reg === 'refused') {
    return { label: 'registration REFUSED — not healthy', className: 'badge badge-red state-impaired', known: true, terminal: false }
  }
  if (state !== 'healthy') {
    const where = chain.failing_hop?.id ? ` (failing hop: ${chain.failing_hop.id})` : ''
    const label = state === 'down' ? 'down' : 'degraded'
    return { label: `${label}${where}`, className: 'badge badge-red state-impaired', known: state !== '', terminal: false }
  }
  if (reg === 'unanswered') {
    return { label: 'registration unanswered', className: 'badge badge-yellow state-idle', known: true, terminal: false }
  }
  return { label: 'healthy', className: 'badge badge-green state-connected', known: true, terminal: false }
}

/**
 * rollupChip is §5.3 rule 6 at glance level: the system is never "ok" while an
 * engaged uplink carries nothing. Green requires total>0 AND
 * carrying === connected > 0.
 */
export function rollupChip(total?: number, connected?: number, carrying?: number): Chip {
  if (total === undefined || connected === undefined || carrying === undefined) {
    return { label: 'uplinks: unknown', className: 'badge badge-gray state-unknown', known: false, terminal: false }
  }
  if (total === 0) {
    return { label: 'uplinks: none configured', className: 'badge badge-gray state-unknown', known: true, terminal: false }
  }
  if (connected === 0) {
    return { label: `uplinks: 0/${total} connected`, className: 'badge badge-red state-impaired', known: true, terminal: false }
  }
  if (carrying < connected) {
    return {
      label: `uplinks: ${connected}/${total} up, ${carrying} carrying`,
      className: 'badge badge-red state-impaired',
      known: true,
      terminal: false,
    }
  }
  return { label: `uplinks: ${connected}/${total} up, ${carrying} carrying`, className: 'badge badge-green state-carrying', known: true, terminal: false }
}

// ---------------------------------------------------------------------------
// §3.2 the add-uplink payload — the SAME shape the API accepts.
// ---------------------------------------------------------------------------

export interface UplinkForm {
  id: string
  /** Host as typed: bare hostname or IP, no scheme (the scheme is its own field). */
  host: string
  port: string
  token: string
  scheme: 'wss' | 'ws'
  /** Dial discipline (§3.1). Independent of `uplink` (§3.2 NOTE). */
  uplinkType: UplinkType
  scheduleStart: string
  scheduleEnd: string
  /** P25-05R: wire this peer as the forward target. NOT coupled to uplinkType. */
  uplink: boolean
}

export const PEER_ID_RE = /^[A-Za-z0-9._-]{1,64}$/
const HHMM_RE = /^([01]\d|2[0-3]):[0-5]\d$/

/**
 * buildUplinkPayload turns the form into the exact POST /api/v1/peers body from
 * spec §3.2. It is a pure function so the test can assert the payload equals the
 * documented API shape field-for-field — no parallel payload, no client-invented
 * keys. `schedule_start`/`schedule_end` appear ONLY for uplink_type=scheduled,
 * as the spec writes them. Returns an error instead of a half-valid body: the
 * server's typed refusals (PEER_ID_INVALID, PEER_UPSTREAM_INVALID,
 * PEER_SCHEDULE_INVALID) are not worth a round-trip to discover.
 */
export function buildUplinkPayload(f: UplinkForm): { payload?: Record<string, unknown>; error?: string } {
  const id = f.id.trim()
  if (!PEER_ID_RE.test(id)) return { error: 'id must match [A-Za-z0-9._-]{1,64}' }

  const host = f.host.trim()
  if (!host) return { error: 'host is required' }
  if (host.includes('://')) return { error: 'host must not include a scheme — pick wss/ws instead' }
  if (/\s/.test(host)) return { error: 'host must not contain spaces' }

  const portNum = Number(f.port.trim())
  if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
    return { error: 'port must be an integer 1–65535' }
  }
  if (!f.token) return { error: 'token is required — the peer token is sealed server-side' }

  if (f.uplinkType === 'scheduled') {
    if (!HHMM_RE.test(f.scheduleStart) || !HHMM_RE.test(f.scheduleEnd)) {
      return { error: 'scheduled requires start and end as HH:MM (server-local; start>end wraps midnight)' }
    }
  }

  const payload: Record<string, unknown> = {
    id,
    upstream: `${f.scheme}://${host}:${portNum}`,
    token: f.token,
    enabled: true,
    uplink: f.uplink,
    uplink_type: f.uplinkType,
  }
  if (f.uplinkType === 'scheduled') {
    payload.schedule_start = f.scheduleStart
    payload.schedule_end = f.scheduleEnd
  }
  return { payload }
}

/**
 * rowIsEditable: bootstrap rows are read-only in this view (§1.1) — a row that
 * did not come from the peers store has no PUT/DELETE round-trip, so the edit
 * affordances are hidden rather than wired to a 404.
 */
export function rowIsEditable(row: Pick<ServerEntry, 'source'>): boolean {
  return (row.source ?? '') === 'peers'
}

/** canDial is the §3.3 rule: only `manual` needs the operator's dial button. */
export function canDial(row: Pick<ServerEntry, 'uplink_type' | 'state' | 'enabled' | 'source'>): boolean {
  if (!rowIsEditable(row)) return false
  if (!row.enabled) return false
  return (row.uplink_type ?? '') === 'manual' && (row.state ?? '') !== 'connected'
}
