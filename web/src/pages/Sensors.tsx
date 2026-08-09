import { useState, useEffect, useMemo } from 'react'
import { api } from '../api/client'
import type { AgentRecord, SensorInfo, SensorAssignment } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'

// Built-in catalog (server keeps a copy but we ship it client-side too for
// the enable/disable flow). The server is the source of truth — values
// here are metadata for the UI; the server validates sensor names.
const SENSOR_CATALOG: SensorInfo[] = [
  { name: 'process_detail', category: 'process', description: 'Agent process info: pid, uid, gid, hostname, working dir, executable, args/env counts' },
  { name: 'runtime_metrics', category: 'process', description: 'Go runtime: go_version, GOOS, GOARCH, NumCPU, NumGoroutine, GOMAXPROCS' },
  { name: 'memory_stats', category: 'process', description: 'Go memory: alloc, total_alloc, sys, heap_alloc, heap_inuse, num_gc, gc_pause_total_ns' },
  { name: 'disk_usage', category: 'filesystem', description: 'Per-path disk usage (total/free bytes). Args: path', args: ['path'] },
  { name: 'file_stat', category: 'filesystem', description: 'Stat a single file/dir (size, mode, mtime). Args: path', args: ['path'] },
  { name: 'env_vars', category: 'filesystem', description: 'Filtered env vars (PATH, HOME, USER, TZ, PWD, SHELL, TERM, HOSTNAME, EDITOR)' },
  { name: 'network_interfaces', category: 'network', description: 'List of network interfaces with flags, MAC, and IPs' },
  { name: 'dns_resolve', category: 'network', description: 'Resolve a hostname to IPs. Args: hostname', args: ['hostname'] },
  { name: 'dns_resolve_mx', category: 'network', description: 'Resolve MX records. Args: hostname', args: ['hostname'] },
  { name: 'dns_resolve_txt', category: 'network', description: 'Resolve TXT records. Args: hostname', args: ['hostname'] },
  { name: 'network_dial', category: 'network', description: 'TCP dial a target and report latency. Args: target, timeout_ms', args: ['target', 'timeout_ms'] },
  { name: 'system_time', category: 'time', description: 'Current UTC time (RFC3339) + Unix epoch' },
  { name: 'uptime', category: 'time', description: 'Agent process uptime in seconds' },
  { name: 'ntp_drift', category: 'time', description: 'NTP query and clock drift vs local (ms). Args: server (default time.google.com:123)', args: ['server'] },
  { name: 'agent_metrics', category: 'agent', description: 'Agent internal counters: messages sent/received, reconnects, uptime' },
  { name: 'audit_chain', category: 'agent', description: 'Hash of agent identity + start time (tamper detection)' },

  // INPUT category — OS-dependent, requires appropriate permissions
  { name: 'active_window', category: 'input', description: 'Title of the foreground window (Linux/Windows/macOS)' },
  { name: 'clipboard_read', category: 'input', description: 'Read the OS clipboard (raw text, no redaction)' },
  { name: 'browser_history', category: 'input', description: 'Most recent N visits from default browser (default 50, max 1000). Requires sqlite3 CLI.' },
  { name: 'keypress_window', category: 'input', description: 'Rolling buffer of recent keystrokes (Linux/Windows only, macOS denied)' },
]

const CATEGORIES = ['process', 'filesystem', 'network', 'time', 'agent', 'input'] as const

export default function Sensors() {
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [selectedAgent, setSelectedAgent] = useState<string>('')
  const [assignment, setAssignment] = useState<SensorAssignment | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('')
  const [categoryFilter, setCategoryFilter] = useState<string>('all')
  const [enabledOnly, setEnabledOnly] = useState(false)
  const [pendingArgs, setPendingArgs] = useState<{ sensor: string; args: string } | null>(null)

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
        // If sensor needs args, prompt for them first
        const sensorInfo = SENSOR_CATALOG.find((s) => s.name === name)
        if (sensorInfo?.args && sensorInfo.args.length > 0 && Object.keys(args).length === 0) {
          setPendingArgs({ sensor: name, args: '' })
          setBusy(false)
          return
        }
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

  const submitArgs = async () => {
    if (!pendingArgs) return
    const [sensor, argsStr] = [pendingArgs.sensor, pendingArgs.args]
    setPendingArgs(null)
    setBusy(true)
    try {
      let parsed: Record<string, string> = {}
      if (argsStr.trim()) {
        // Parse key=value pairs separated by commas or spaces
        for (const part of argsStr.split(/[\s,]+/)) {
          const [k, ...rest] = part.split('=')
          if (k && rest.length > 0) parsed[k] = rest.join('=')
        }
      }
      await api.enableSensor(selectedAgent, sensor, parsed)
      const updated = await api.getAgentSensors(selectedAgent)
      setAssignment(updated)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  // Compute metrics
  const metrics = useMemo(() => {
    const enabledNames = Object.entries(assignment?.sensors || {}).filter(([, v]) => v.enabled).map(([k]) => k)
    return {
      enabledCount: enabledNames.length,
      totalCount: SENSOR_CATALOG.length,
      byCategory: CATEGORIES.map(cat => ({
        cat,
        total: SENSOR_CATALOG.filter(s => s.category === cat).length,
        enabled: SENSOR_CATALOG.filter(s => s.category === cat && enabledNames.includes(s.name)).length,
      })),
    }
  }, [assignment])

  const filteredSensors = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return SENSOR_CATALOG.filter(s => {
      if (categoryFilter !== 'all' && s.category !== categoryFilter) return false
      if (enabledOnly && !assignment?.sensors[s.name]?.enabled) return false
      if (q && !s.name.includes(q) && !s.description.toLowerCase().includes(q)) return false
      return true
    })
  }, [filter, categoryFilter, enabledOnly, assignment])

  const selectedAgentObj = agents.find(a => a.agent_id === selectedAgent)

  return (
    <div>
      <div className="page-header-row">
        <div style={{ flex: 1, minWidth: 280 }}>
          <h1>Sensors</h1>
          <div className="page-subtitle">
            Per-agent capability toggles. Sensors are stateless primitives that a flow can invoke via <code style={{ background: 'var(--bg-input)', padding: '1px 6px', borderRadius: 3 }}>command_type</code> or that you can read on demand.
          </div>
          <div className="page-meta" style={{ marginTop: 12 }}>
            <span className="meta-item">
              <span className="meta-value" style={{ color: metrics.enabledCount > 0 ? 'var(--green)' : 'var(--text-muted)' }}>
                {metrics.enabledCount}
              </span>
              <span style={{ opacity: 0.6 }}>of {metrics.totalCount} enabled</span>
            </span>
            {metrics.byCategory.map(({ cat, total, enabled }) => (
              <span key={cat} className="meta-item">
                <span className="meta-value" style={{ color: enabled > 0 ? 'var(--green-dim)' : 'var(--text-dim)', fontSize: 11 }}>
                  {enabled}/{total}
                </span>
                <span style={{ opacity: 0.6, fontSize: 11 }}>{cat}</span>
              </span>
            ))}
          </div>
        </div>
        <div className="page-actions">
          <select
            className="toolbar-input"
            value={selectedAgent}
            onChange={(e) => setSelectedAgent(e.target.value)}
            style={{ minWidth: 200 }}
          >
            {agents.length === 0 && <option value="">(no agents connected)</option>}
            {agents.map((a) => (
              <option key={a.agent_id} value={a.agent_id}>{a.name} · {a.status}</option>
            ))}
          </select>
        </div>
      </div>

      {error && <div className="badge badge-red" style={{ marginBottom: 16, padding: '8px 14px', fontSize: 12 }}>Error: {error}</div>}

      {!selectedAgentObj ? (
        <div className="empty-state">
          <h3>No agent selected</h3>
          <p>Sensors are configured per-agent. Wait for an agent to connect, then select it from the dropdown above.</p>
        </div>
      ) : (
        <>
          {/* Category summary chips */}
          <div className="category-chips">
            <button
              className={`category-chip ${categoryFilter === 'all' && !enabledOnly ? 'active' : ''}`}
              onClick={() => { setCategoryFilter('all'); setEnabledOnly(false) }}
            >
              all
              <span className="chip-count">{metrics.totalCount}</span>
            </button>
            {metrics.byCategory.map(({ cat, total, enabled }) => (
              <button
                key={cat}
                className={`category-chip ${categoryFilter === cat && !enabledOnly ? 'active' : ''}`}
                onClick={() => { setCategoryFilter(cat); setEnabledOnly(false) }}
              >
                {cat}
                <span className="chip-count">{enabled}/{total}</span>
              </button>
            ))}
            <button
              className={`category-chip ${enabledOnly ? 'active' : ''}`}
              onClick={() => { setEnabledOnly(o => !o); setCategoryFilter('all') }}
              style={{ borderColor: enabledOnly ? 'var(--green)' : 'var(--border)' }}
            >
              ✓ enabled only
              <span className="chip-count">{metrics.enabledCount}</span>
            </button>
          </div>

          {/* Search + filter row */}
          <div className="toolbar">
            <input
              className="toolbar-input"
              placeholder="Search by name or description..."
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              style={{ flex: 1, maxWidth: 360 }}
            />
            <span style={{ color: 'var(--text-muted)', fontSize: 12, fontFamily: 'var(--font-mono)' }}>
              {filteredSensors.length} of {SENSOR_CATALOG.length}
            </span>
          </div>

          {/* Unified table — single header, category column for context */}
          <div className="card" style={{ padding: 0 }}>
            <div className="table-container">
              <table>
                <thead>
                  <tr>
                    <th>Sensor</th>
                    <th>Category</th>
                    <th>Description</th>
                    <th>Status</th>
                    <th style={{ width: 110, textAlign: 'right' }}>Action</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredSensors.length === 0 ? (
                    <tr>
                      <td colSpan={5} style={{ textAlign: 'center', padding: 32, color: 'var(--text-muted)' }}>
                        No sensors match the current filter.
                      </td>
                    </tr>
                  ) : filteredSensors.map((s) => {
                    const state = assignment?.sensors[s.name]
                    const enabled = state?.enabled || false
                    const needsArg = !!(s.args && s.args.length > 0)
                    return (
                      <tr key={s.name}>
                        <td>
                          <div style={{ display: 'flex', alignItems: 'center' }}>
                            <span className={`row-status-bar ${enabled ? 'green' : 'gray'}`} />
                            <code style={{ fontWeight: 600 }}>{s.name}</code>
                            {needsArg && !enabled && (
                              <span className="needs-arg" title={`Requires: ${s.args?.join(', ')}`} style={{ marginLeft: 8 }}>
                                ⚙ needs args
                              </span>
                            )}
                          </div>
                          {state?.args && Object.keys(state.args).length > 0 && (
                            <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)', paddingLeft: 14 }}>
                              {Object.entries(state.args).map(([k, v]) => (
                                <span key={k} style={{ marginRight: 8 }}>
                                  <span style={{ color: 'var(--green-dim)' }}>{k}</span>={v}
                                </span>
                              ))}
                            </div>
                          )}
                        </td>
                        <td>
                          <span className="trigger-badge once" style={{ textTransform: 'uppercase' }}>{s.category}</span>
                        </td>
                        <td style={{ color: 'var(--text-muted)', fontSize: 12, maxWidth: 480 }} className="truncate">
                          {s.description}
                        </td>
                        <td>
                          <StatusBadge status={enabled ? 'active' : 'inactive'} />
                        </td>
                        <td style={{ textAlign: 'right' }}>
                          <button
                            className={enabled ? 'btn btn-sm' : 'btn btn-sm btn-primary'}
                            disabled={busy || !selectedAgent}
                            onClick={() => toggleSensor(s.name, enabled, state?.args || {})}
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
          </div>

          <div style={{ opacity: 0.6, fontSize: 12, marginTop: 18, color: 'var(--text-muted)', textAlign: 'center' }}>
            All sensors are OS-independent — same catalog runs on Windows, Linux, macOS, Android.
            <br />
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>disk_usage & file_stat</span> use stdlib path-based APIs.
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, marginLeft: 16 }}>browser_history</span> requires <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11 }}>sqlite3</span> CLI on agent.
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, marginLeft: 16 }}>keypress_window</span> requires elevated permissions (input group / root on Linux).
          </div>
        </>
      )}

      {/* Args prompt modal */}
      {pendingArgs && (
        <div style={{
          position: 'fixed', inset: 0,
          background: 'rgba(0,0,0,0.7)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          zIndex: 1000,
        }}>
          <div className="card" style={{ width: 480, padding: 24, margin: 0 }}>
            <div className="card-title">Enable {pendingArgs.sensor}</div>
            <p style={{ color: 'var(--text-muted)', fontSize: 13, marginBottom: 14 }}>
              This sensor requires arguments. Enter as <code style={{ background: 'var(--bg-input)', padding: '1px 6px', borderRadius: 3 }}>key=value</code> separated by spaces or commas. Required keys for this sensor:
            </p>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--green-dim)', marginBottom: 12 }}>
              {SENSOR_CATALOG.find(s => s.name === pendingArgs.sensor)?.args?.join(' · ')}
            </div>
            <input
              className="toolbar-input"
              style={{ width: '100%', minWidth: 0 }}
              placeholder="path=/root key=value ..."
              value={pendingArgs.args}
              onChange={(e) => setPendingArgs({ ...pendingArgs, args: e.target.value })}
              autoFocus
              onKeyDown={(e) => { if (e.key === 'Enter') submitArgs() }}
            />
            <div style={{ display: 'flex', gap: 8, marginTop: 16, justifyContent: 'flex-end' }}>
              <button className="btn" onClick={() => setPendingArgs(null)}>Cancel</button>
              <button className="btn btn-primary" onClick={submitArgs}>Enable</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}