import { useState, useEffect } from 'react'
import { api } from '../api/client'
import type { AgentRecord, SensorInfo, SensorAssignment } from '../api/types'

// Built-in catalog (server keeps a copy but we ship it client-side too for
// the enable/disable flow). The server is the source of truth — values
// here are metadata for the UI; the server validates sensor names.
const SENSOR_CATALOG: SensorInfo[] = [
  { name: 'process_detail', category: 'process', description: 'Agent process info: pid, uid, gid, hostname, working dir, executable, args/env counts' },
  { name: 'runtime_metrics', category: 'process', description: 'Go runtime: go_version, GOOS, GOARCH, NumCPU, NumGoroutine, GOMAXPROCS' },
  { name: 'memory_stats', category: 'process', description: 'Go memory: alloc, total_alloc, sys, heap_alloc, heap_inuse, num_gc, gc_pause_total_ns' },
  { name: 'disk_usage', category: 'filesystem', description: 'Per-path disk usage (total/free bytes). Args: path' },
  { name: 'file_stat', category: 'filesystem', description: 'Stat a single file/dir (size, mode, mtime). Args: path' },
  { name: 'env_vars', category: 'filesystem', description: 'Filtered env vars (PATH, HOME, USER, TZ, PWD, SHELL, TERM, HOSTNAME, EDITOR)' },
  { name: 'network_interfaces', category: 'network', description: 'List of network interfaces with flags, MAC, and IPs' },
  { name: 'dns_resolve', category: 'network', description: 'Resolve a hostname to IPs. Args: hostname' },
  { name: 'dns_resolve_mx', category: 'network', description: 'Resolve MX records. Args: hostname' },
  { name: 'dns_resolve_txt', category: 'network', description: 'Resolve TXT records. Args: hostname' },
  { name: 'network_dial', category: 'network', description: 'TCP dial a target and report latency. Args: target, timeout_ms' },
  { name: 'system_time', category: 'time', description: 'Current UTC time (RFC3339) + Unix epoch' },
  { name: 'uptime', category: 'time', description: 'Agent process uptime in seconds' },
  { name: 'ntp_drift', category: 'time', description: 'NTP query and clock drift vs local (ms). Args: server (default time.google.com:123)' },
  { name: 'agent_metrics', category: 'agent', description: 'Agent internal counters: messages sent/received, reconnects, uptime' },
  { name: 'audit_chain', category: 'agent', description: 'Hash of agent identity + start time (tamper detection)' },
]

const CATEGORIES = ['process', 'filesystem', 'network', 'time', 'agent']

export default function Sensors() {
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [selectedAgent, setSelectedAgent] = useState<string>('')
  const [assignment, setAssignment] = useState<SensorAssignment | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // Load agents on mount
  useEffect(() => {
    api.listAgents().then((a) => {
      const list = Array.isArray(a) ? a : []
      setAgents(list)
      if (list.length > 0 && !selectedAgent) {
        setSelectedAgent(list[0].agent_id)
      }
    }).catch((e) => setError((e as Error).message))
  }, [])

  // Load assignment when agent changes
  useEffect(() => {
    if (!selectedAgent) return
    api.getAgentSensors(selectedAgent)
      .then((a) => setAssignment(a))
      .catch((e) => setError((e as Error).message))
  }, [selectedAgent])

  const toggleSensor = async (name: string, enabled: boolean, args: Record<string, string>) => {
    if (!selectedAgent) return
    setBusy(true)
    setError('')
    try {
      if (enabled) {
        await api.disableSensor(selectedAgent, name)
      } else {
        await api.enableSensor(selectedAgent, name, args)
      }
      const updated = await api.getAgentSensors(selectedAgent)
      setAssignment(updated)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ padding: 24, color: '#00ff41', fontFamily: 'monospace' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ margin: 0, textShadow: '0 0 8px #00ff41' }}>Sensors</h1>
      </div>

      {error && <div style={{ color: '#ff6b6b', marginBottom: 16 }}>Error: {error}</div>}

      <div style={{ marginBottom: 16 }}>
        <label style={{ marginRight: 8 }}>Agent:</label>
        <select
          value={selectedAgent}
          onChange={(e) => setSelectedAgent(e.target.value)}
          style={{
            background: '#0a0a0a',
            border: '1px solid #00ff4166',
            color: '#00ff41',
            padding: '4px 8px',
            fontFamily: 'monospace',
            fontSize: 13,
            minWidth: 240,
          }}
        >
          {agents.length === 0 && <option value="">(no agents connected)</option>}
          {agents.map((a) => (
            <option key={a.agent_id} value={a.agent_id}>{a.name} ({a.status})</option>
          ))}
        </select>
        <span style={{ marginLeft: 16, opacity: 0.7 }}>
          {assignment ? `${Object.keys(assignment.sensors).length} sensors configured` : '...'}
        </span>
      </div>

      {CATEGORIES.map((cat) => {
        const cat_sensors = SENSOR_CATALOG.filter((s) => s.category === cat)
        return (
          <div key={cat} style={{ marginBottom: 24 }}>
            <h2 style={{ borderBottom: '1px solid #00ff4144', paddingBottom: 4 }}>{cat.toUpperCase()}</h2>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
              <thead>
                <tr>
                  <th style={th}>Name</th>
                  <th style={th}>Description</th>
                  <th style={th}>Status</th>
                  <th style={th}>Action</th>
                </tr>
              </thead>
              <tbody>
                {cat_sensors.map((s) => {
                  const state = assignment?.sensors[s.name]
                  const enabled = state?.enabled || false
                  return (
                    <tr key={s.name}>
                      <td style={td}><code>{s.name}</code></td>
                      <td style={td}>{s.description}</td>
                      <td style={td}>
                        <span style={{
                          color: enabled ? '#00ff41' : '#666',
                          fontWeight: 'bold',
                        }}>{enabled ? 'ENABLED' : 'OFF'}</span>
                        {state?.args && Object.keys(state.args).length > 0 && (
                          <div style={{ fontSize: 11, opacity: 0.6 }}>args: {JSON.stringify(state.args)}</div>
                        )}
                      </td>
                      <td style={td}>
                        <button
                          disabled={busy || !selectedAgent}
                          onClick={() => toggleSensor(s.name, enabled, state?.args || {})}
                          style={enabled ? btnDanger : btnPrimary}
                        >
                          {enabled ? 'Disable' : 'Enable'}
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )
      })}

      <div style={{ opacity: 0.6, fontSize: 12, marginTop: 24 }}>
        Sensors are OS-independent — same catalog runs on Windows, Linux, macOS, Android.
        'disk_usage' and 'file_stat' use stdlib path-based APIs (os.DiskUsage / syscall.Statfs).
      </div>
    </div>
  )
}

const th = { textAlign: 'left' as const, borderBottom: '1px solid #00ff4144', padding: 6, color: '#00ff41aa' }
const td = { borderBottom: '1px solid #00ff4122', padding: 6, verticalAlign: 'top' as const }
const btn = {
  background: 'transparent',
  border: '1px solid #00ff41',
  color: '#00ff41',
  padding: '4px 10px',
  cursor: 'pointer',
  fontFamily: 'monospace',
  fontSize: 12,
} as const
const btnPrimary = { ...btn, background: '#00ff4122', fontWeight: 'bold' as const }
const btnDanger = { ...btn, borderColor: '#ff6b6b', color: '#ff6b6b' }