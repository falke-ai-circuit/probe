import { useCallback, useEffect, useState } from 'react'
import { api } from '../api/client'
import type { HealthInfo, ServerEntry } from '../api/types'

// P18-06: shell-level host badge — ONE component, TWO variants.
//
//  variant="shell"  authenticated. Consumes the local inventory entry
//    (GET /api/v1/servers, P21-02/P22-02): servers[local_id] is THIS process —
//    the same projection the Network page reads, never a second computation.
//  variant="login"  pre-auth. GET /api/v1/health answers without a token
//    (identity block, P22-02 §3.1). Fields /health cannot answer render the
//    literal UNRESOLVED — an invented name here is a falsifiable defect.
//
// Every value is live server data or UNRESOLVED, never a blank and never a
// build constant. A failed refresh keeps the last-known values and shows the
// degraded chip; an unreachable backend is never a blank row or a crash.
const UNRESOLVED = 'UNRESOLVED'

// fmtUptime renders seconds as a compact human duration (live data only).
function fmtUptime(s: number): string {
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

function normVersion(v?: string | null): string {
  if (!v) return UNRESOLVED
  return v.startsWith('v') ? v : `v${v}`
}

function Field({ label, value, testid }: { label: string; value: string; testid: string }) {
  return (
    <span className="meta-item" data-testid={testid}>
      <span className="dim">{label}</span>
      <span className={value === UNRESOLVED ? 'hb-unresolved' : ''}>{value}</span>
    </span>
  )
}

function RoleChip({ role }: { role?: string | null }) {
  if (!role) {
    return <span className="badge badge-gray" data-testid="hb-role-chip">{UNRESOLVED}</span>
  }
  // Role chip derives ONLY from the inventory role field (LOCAL/PEER).
  // PEER means this instance is itself somebody's peer; role=local renders
  // as LOCAL without guessing any upstream.
  return (
    <span className={`badge ${role === 'peer' ? 'badge-blue' : 'badge-green'}`} data-testid="hb-role-chip">
      {role.toUpperCase()}
    </span>
  )
}

function DegradedChip({ text }: { text: string }) {
  return <span className="badge badge-red" data-testid="hb-degraded">{text}</span>
}

function ShellBadge() {
  const [entry, setEntry] = useState<ServerEntry | null>(null)
  const [unreachable, setUnreachable] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const resp = await api.listServers()
      const local = resp.servers.find(s => s.id === resp.local_id) || resp.servers[0] || null
      setEntry(local)
      setUnreachable(false) // backend reachable again — degraded chip clears
    } catch {
      setUnreachable(true) // keep last-known entry visible, add degraded chip
    }
  }, [])

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 30000)
    return () => clearInterval(t)
  }, [refresh])

  const down = unreachable ? 'UNREACHABLE' : entry && entry.state !== 'up' ? `DEGRADED:${entry.state}` : ''
  const counts = entry?.counts

  return (
    <div className="host-badge" data-testid="host-badge">
      {down && <DegradedChip text={down} />}
      <Field label="host" testid="hb-name" value={entry?.name || UNRESOLVED} />
      <Field label="addr" testid="hb-addr" value={entry?.listen_addr || UNRESOLVED} />
      <Field label="version" testid="hb-version" value={normVersion(entry?.version)} />
      <RoleChip role={entry?.role} />
      <details>
        <summary>details</summary>
        <div className="hb-expand">
          <Field
            label="uptime"
            testid="hb-uptime"
            value={entry?.uptime_seconds != null ? fmtUptime(entry.uptime_seconds) : UNRESOLVED}
          />
          <Field
            label="agents (direct/relayed)"
            testid="hb-agents"
            value={counts ? `${counts.direct_agents}/${counts.relayed_agents}` : UNRESOLVED}
          />
          <Field
            label="forward_policy"
            testid="hb-policy"
            value={entry?.forward_policy || UNRESOLVED}
          />
        </div>
      </details>
    </div>
  )
}

function LoginBadge() {
  const [health, setHealth] = useState<HealthInfo | null>(null)
  const [unreachable, setUnreachable] = useState(false)

  const refresh = useCallback(async () => {
    try {
      setHealth(await api.getHealth()) // GET /api/v1/health answers WITHOUT auth
      setUnreachable(false)
    } catch {
      setUnreachable(true)
    }
  }, [])

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 30000)
    return () => clearInterval(t)
  }, [refresh])

  // identity is null when PROBE_PREAUTH_IDENTITY=off — every identity field
  // then renders UNRESOLVED rather than a guess.
  const identity = health?.identity || null

  return (
    <div className="host-badge" data-testid="host-badge">
      {unreachable && <DegradedChip text="UNREACHABLE" />}
      <Field label="host" testid="hb-name" value={identity?.name || UNRESOLVED} />
      <Field label="addr" testid="hb-addr" value={identity?.listen_addr || UNRESOLVED} />
      <Field label="version" testid="hb-version" value={normVersion(identity?.version)} />
      <RoleChip role={identity?.role} />
      <details>
        <summary>details</summary>
        <div className="hb-expand">
          <Field
            label="uptime"
            testid="hb-uptime"
            value={health?.uptime_seconds != null ? fmtUptime(health.uptime_seconds) : UNRESOLVED}
          />
          <Field
            label="agents (total)"
            testid="hb-agents"
            value={health ? String(health.total_agents) : UNRESOLVED}
          />
          <Field label="agents (direct/relayed)" testid="hb-agents-split" value={UNRESOLVED} />
          <Field label="forward_policy" testid="hb-policy" value={UNRESOLVED} />
        </div>
      </details>
    </div>
  )
}

export default function HostBadge({ variant }: { variant: 'shell' | 'login' }) {
  return variant === 'login' ? <LoginBadge /> : <ShellBadge />
}
