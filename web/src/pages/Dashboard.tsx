import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { HealthInfo, AgentRecord, Task, ServerEntry } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'
import { TopologyGraph } from '../components/TopologyGraph'
import { rollupChip, carryingVerdict, stateChip, encryptionBadge } from '../lib/servers-view'

export default function Dashboard() {
  const navigate = useNavigate()
  const [health, setHealth] = useState<HealthInfo | null>(null)
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [servers, setServers] = useState<ServerEntry[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    const load = async () => {
      try {
        const [h, a, t, s] = await Promise.all([
          api.getHealth(),
          api.listAgents(),
          api.listTasks(),
          // P27-03: the landing page carries the uplink roll-up too — a
          // glance-level "ok" must not print while an uplink carries nothing
          // (spec §5.3 rule 6). A failed /servers read leaves the panel empty
          // and the chip unknown; it never fabricates a healthy fleet.
          api.listServers().catch(() => null),
        ])
        setHealth(h)
        setAgents((a || []).sort((x, y) => (x.name || x.agent_id).localeCompare(y.name || y.agent_id)))
        setTasks(t || [])
        setServers(s?.servers || [])
      } catch (e) {
        setError((e as Error).message)
      }
    }
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  // The full fleet lives on /agents and the task list on /tasks; the landing
  // page only carries the operational signal — agents that need a human.
  // Capped: an all-inactive fleet must not turn the panel back into the
  // 26-row table that /agents already owns.
  const ATTENTION_LIMIT = 8
  const attentionAgents = (agents || []).filter(a => a.status === 'stale' || a.status === 'inactive')
  const shownAttention = attentionAgents.slice(0, ATTENTION_LIMIT)

  // P27-03 (§5.3 rule 6): the glance-level uplift. Green here requires every
  // connected uplink to be carrying; anything else is amber or red, and an
  // absent roll-up (older server) is unknown — never green by default.
  const statusRollup = rollupChip(health?.uplinks_total, health?.uplinks_connected, health?.uplinks_carrying)
  // The landing panel lists the NON-local rows: servers[0] is this process and
  // already shown in the shell host badge. role is the server's own word for it.
  const uplinkRows = (servers || []).filter(s => s.role !== 'local')

  return (
    <div>
      <div className="page-header">
        <h1>Dashboard</h1>
        <p>PROBE server overview and quick stats</p>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <TopologyGraph />

      <div className="stat-grid">
        <div className="stat-card">
          <div className="stat-label">Server Status</div>
          {/* P27-03 (§5.3 rule 6): the old cell printed health.status green
              unconditionally, so it read healthy while an uplink was connected
              and carrying nothing. The value is now the roll-up, and the colour
              comes from the same binding — green only when every connected
              uplink carries. */}
          <div
            className={`stat-value ${statusRollup.className.includes('badge-green') ? 'green' : statusRollup.className.includes('badge-red') ? 'red' : statusRollup.className.includes('badge-yellow') ? 'yellow' : ''}`}
            data-testid="dash-server-status"
          >
            {health?.status || '…'}
          </div>
          <div className="stat-sub mono dim" data-testid="dash-uplink-rollup">{statusRollup.label}</div>
          {health?.server_version && (
            <div className="stat-sub mono dim">{health.server_version.startsWith('v') ? health.server_version : `v${health.server_version}`}</div>
          )}
        </div>
        <div className="stat-card">
          <div className="stat-label">Total Agents</div>
          <div className="stat-value">{health?.total_agents ?? '…'}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Active Agents</div>
          <div className="stat-value green">{health?.active_agents ?? '…'}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Stale Agents</div>
          <div className="stat-value yellow">{health?.stale_agents ?? '…'}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Uptime</div>
          <div className="stat-value">
            {health ? formatUptime(health.uptime_seconds) : '…'}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Total Tasks</div>
          <div className="stat-value">{tasks.length}</div>
        </div>
      </div>

      <div className="card">
        <div className="card-title" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
          <span>Servers — uplinks ({uplinkRows.length})</span>
          <Link to="/network" className="btn btn-sm">Uplink detail →</Link>
        </div>
        {uplinkRows.length === 0 ? (
          <div className="empty-state" data-testid="dash-uplinks-empty">
            No uplinks configured. Add one on Network.
          </div>
        ) : (
          <div className="table-container">
            <table data-testid="dash-uplinks-table">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Direction</th>
                  <th>State</th>
                  <th>Encrypted</th>
                  <th>Carrying</th>
                </tr>
              </thead>
              <tbody>
                {uplinkRows.map(s => {
                  const st = stateChip(s.state)
                  const carry = carryingVerdict(s)
                  const enc = encryptionBadge(s.encrypted)
                  return (
                    <tr key={s.id} data-testid={`dash-server-${s.id}`} data-state={s.state}>
                      <td className="mono">{s.name || s.id}</td>
                      <td className="mono dim">{s.direction || 'unknown'}</td>
                      <td><span className={st.className} data-testid="dash-server-state">{st.label}</span></td>
                      <td><span className={enc.className} data-testid="dash-server-encrypted">{enc.label}</span></td>
                      <td><span className={carry.chip.className} data-testid="dash-server-carrying">{carry.chip.label}</span></td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
          <span>Needs attention</span>
          <span className="flex gap-8">
            <Link to="/agents" className="btn btn-sm">All agents →</Link>
            <Link to="/tasks" className="btn btn-sm">All tasks →</Link>
          </span>
        </div>
        {attentionAgents.length === 0 ? (
          <div className="empty-state">No stale or inactive agents — the full fleet list is on Agents.</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Health</th>
                  <th>Last heartbeat</th>
                </tr>
              </thead>
              <tbody>
                {shownAttention.map(a => {
                  // O4: agent ids may contain '/', which would be read as a path
                  // separator and fall through to the wildcard route.
                  const to = `/agents/${encodeURIComponent(a.agent_id)}`
                  return (
                    <tr key={a.agent_id} className="clickable" onClick={() => navigate(to)}>
                      <td>
                        <Link to={to} onClick={e => e.stopPropagation()}>{a.name || a.agent_id}</Link>
                      </td>
                      <td><StatusBadge status={a.status} /></td>
                      <td>{(a.health_score * 100).toFixed(0)}%</td>
                      <td className="dim">{a.last_heartbeat ? new Date(a.last_heartbeat).toLocaleString() : '—'}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {attentionAgents.length > shownAttention.length && (
          <p className="dim mt-16" style={{ fontSize: 12 }} id="attention-more">
            +{attentionAgents.length - shownAttention.length} more stale or inactive agents — see All agents.
          </p>
        )}
      </div>

    </div>
  )
}

function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
  return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`
}