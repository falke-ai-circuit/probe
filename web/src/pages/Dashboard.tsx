import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { HealthInfo, AgentRecord, Task } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'
import { TopologyGraph } from '../components/TopologyGraph'

export default function Dashboard() {
  const [health, setHealth] = useState<HealthInfo | null>(null)
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    const load = async () => {
      try {
        const [h, a, t] = await Promise.all([
          api.getHealth(),
          api.listAgents(),
          api.listTasks(),
        ])
        setHealth(h)
        setAgents((a || []).sort((x, y) => (x.name || x.agent_id).localeCompare(y.name || y.agent_id)))
        setTasks(t || [])
      } catch (e) {
        setError((e as Error).message)
      }
    }
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  const recentTasks = (tasks || []).slice(0, 10)
  const activeAgents = (agents || []).filter(a => a.status === 'active')
  const staleAgents = (agents || []).filter(a => a.status === 'stale')

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
          <div className="stat-value green">{health?.status || '…'}</div>
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
        <div className="card-title">Connected Agents</div>
        {agents.length === 0 ? (
          <div className="empty-state">No agents connected</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Version</th>
                  <th>OS / Arch</th>
                  <th>Status</th>
                  <th>Health</th>
                  <th>Capabilities</th>
                </tr>
              </thead>
              <tbody>
                {agents.map(a => (
                  <tr key={a.agent_id} className="clickable" onClick={() => window.location.hash = `/agents/${a.agent_id}`}>
                    <td>
                      <Link to={`/agents/${a.agent_id}`}>{a.name || a.agent_id}</Link>
                    </td>
                    <td className="mono green">{a.version || '—'}</td>
                    <td className="mono">{a.os}/{a.arch}</td>
                    <td><StatusBadge status={a.status} /></td>
                    <td>{(a.health_score * 100).toFixed(0)}%</td>
                    <td className="mono dim">{(a.capabilities || []).join(', ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title">Recent Tasks</div>
        {recentTasks.length === 0 ? (
          <div className="empty-state">No tasks</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Task ID</th>
                  <th>Agent</th>
                  <th>Type</th>
                  <th>Schedule</th>
                  <th>Status</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {recentTasks.map(t => (
                  <tr key={t.id}>
                    <td className="mono dim">{t.id.slice(0, 8)}…</td>
                    <td className="mono dim">{t.agent_id.slice(0, 12)}…</td>
                    <td>{t.command_type}</td>
                    <td>{t.schedule.type}</td>
                    <td><StatusBadge status={t.status} /></td>
                    <td className="dim">{new Date(t.created_at).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
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