import { useState, useEffect, useMemo } from 'react'
import { api } from '../api/client'
import type { FlowRecord, FlowTrigger, FlowStep, FlowTemplate, FlowRun, AgentRecord } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'
import { ActionMenu } from '../components/ActionMenu'

const STEP_TYPES = ['command', 'wait', 'branch', 'compute_diff', 'classify', 'emit']
const TRIGGER_TYPES = [
  { value: 'once', label: 'Once' },
  { value: 'delayed', label: 'Delayed' },
  { value: 'recurring', label: 'Recurring' },
]

const emptyStep = (): FlowStep => ({ id: 's1', type: 'command', command_type: 'sysinfo' })
const emptyFlow = (): Partial<FlowRecord> => ({
  name: '',
  description: '',
  enabled: true,
  trigger: { type: 'once' } as FlowTrigger,
  steps: [emptyStep()],
})


// (ActionMenu extracted to web/src/components/ActionMenu.tsx for reuse
//  + viewport-aware positioning that handles clipping + escape-to-close.)

function copyText(text: string) {
  navigator.clipboard?.writeText(text).catch(() => {})
}

// Map a flow run to its row status accent
function statusAccent(s: string) {
  if (s === 'completed') return 'green'
  if (s === 'running') return 'green'
  if (s === 'failed') return 'red'
  if (s === 'pending' || s === 'queued') return 'yellow'
  return 'gray'
}

function shortID(id: string) {
  return id ? id.slice(0, 12) + '…' : '—'
}

export default function Flows() {
  const [flows, setFlows] = useState<FlowRecord[]>([])
  const [templates, setTemplates] = useState<FlowTemplate[]>([])
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<FlowRecord | null>(null)
  const [form, setForm] = useState<Partial<FlowRecord>>(emptyFlow())
  const [templatesOpen, setTemplatesOpen] = useState(false)
  const [flowFilter, setFlowFilter] = useState('')
  const [runFilter, setRunFilter] = useState('')
  const [showRunForFlow, setShowRunForFlow] = useState<string | null>(null)
  // Agent picker modal state
  const [runPickerFlow, setRunPickerFlow] = useState<FlowRecord | null>(null)
  // JSON params error per step index (so the operator can see what went wrong)
  const [paramsErrors, setParamsErrors] = useState<Record<number, string>>({})

  const load = async () => {
    setLoading(true)
    try {
      const [f, t, r, a] = await Promise.all([
        api.listFlows(),
        api.listFlowTemplates(),
        api.listFlowRuns(),
        api.listAgents().catch(() => []),
      ])
      setFlows(Array.isArray(f) ? f : [])
      setTemplates(Array.isArray(t) ? t : [])
      setRuns(Array.isArray(r) ? r : [])
      setAgents(Array.isArray(a) ? a : [])
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  const startCreate = () => {
    setEditing(null)
    setForm(emptyFlow())
    setShowForm(true)
  }
  const startEdit = (flow: FlowRecord) => {
    setEditing(flow)
    setForm({
      name: flow.name,
      description: flow.description,
      enabled: flow.enabled,
      trigger: flow.trigger,
      steps: flow.steps,
    })
    setShowForm(true)
  }

  const save = async () => {
    setError('')
    try {
      if (editing) {
        await api.updateFlow(editing.id, form)
      } else {
        await api.createFlow(form)
      }
      setShowForm(false)
      await load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const remove = async (id: string) => {
    if (busy) return
    if (!confirm('Delete this flow?')) return
    setBusy(true)
    try {
      await api.deleteFlow(id)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const toggleEnabled = async (flow: FlowRecord) => {
    if (busy) return
    setBusy(true)
    try {
      if (flow.enabled) {
        await api.disableFlow(flow.id)
      } else {
        await api.enableFlow(flow.id)
      }
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const runNow = async (flow: FlowRecord, agentID: string) => {
    if (busy) return
    if (!agentID) return
    setBusy(true)
    try {
      await api.runFlowNow(flow.id, agentID)
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const useTemplate = async (name: string) => {
    try {
      const flow = await api.instantiateFromTemplate(name)
      await load()
      startEdit(flow)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const updateStep = (idx: number, patch: Partial<FlowStep>) => {
    const steps = (form.steps || []).map((s, i) => (i === idx ? { ...s, ...patch } : s))
    setForm({ ...form, steps })
  }
  const addStep = () => {
    const steps = [...(form.steps || []), emptyStep()]
    setForm({ ...form, steps })
  }
  const removeStep = (idx: number) => {
    const steps = (form.steps || []).filter((_, i) => i !== idx)
    setForm({ ...form, steps: steps.length ? steps : [emptyStep()] })
  }

  // Derived metrics
  const metrics = useMemo(() => {
    const total = flows.length
    const active = flows.filter(f => f.enabled).length
    const recentRuns = runs.slice(0, 50)
    const succeeded = recentRuns.filter(r => r.status === 'completed').length
    const failed = recentRuns.filter(r => r.status === 'failed').length
    const successRate = recentRuns.length ? Math.round((succeeded / recentRuns.length) * 100) : 0
    return { total, active, recent: recentRuns.length, succeeded, failed, successRate }
  }, [flows, runs])

  const filteredFlows = useMemo(() => {
    const q = flowFilter.trim().toLowerCase()
    return [...flows]
      .sort((a, b) => a.name.localeCompare(b.name))
      .filter(f => !q || f.name.toLowerCase().includes(q) || (f.description || '').toLowerCase().includes(q))
  }, [flows, flowFilter])

  const filteredRuns = useMemo(() => {
    const q = runFilter.trim().toLowerCase()
    return runs
      .filter(r => !showRunForFlow || r.flow_id === showRunForFlow)
      .filter(r => !q || r.flow_id.toLowerCase().includes(q) || r.id.toLowerCase().includes(q) || (r.agent_id || '').toLowerCase().includes(q) || (r.error || '').toLowerCase().includes(q))
      .slice(0, 30)
  }, [runs, runFilter, showRunForFlow])

  return (
    <div>
      <div className="page-header-row">
        <div style={{ flex: 1, minWidth: 280 }}>
          <h1>Flows</h1>
          <div className="page-subtitle">
            Compose server-side workflows from existing commands + sensors. Triggers run on schedule; results stream as survey events.
          </div>
          <div className="page-meta" style={{ marginTop: 12 }}>
            <span className="meta-item">
              <span className="meta-value">{metrics.active}</span>
              <span style={{ opacity: 0.6 }}>/ {metrics.total} active</span>
            </span>
            <span className="meta-item">
              <span className="meta-value" style={{ color: 'var(--green-dim)' }}>
                {metrics.successRate}%
              </span>
              <span style={{ opacity: 0.6 }}>success ({metrics.recent} runs)</span>
            </span>
            <span className="meta-item">
              <span className="meta-value" style={{ color: metrics.failed > 0 ? 'var(--red)' : 'var(--text-dim)' }}>
                {metrics.failed}
              </span>
              <span style={{ opacity: 0.6 }}>failed</span>
            </span>
          </div>
        </div>
        <div className="page-actions">
          <button className="btn btn-primary" onClick={startCreate}>
            <span style={{ fontSize: 16, lineHeight: 1 }}>+</span> New Flow
          </button>
          <button className="btn" onClick={load}>↻ Refresh</button>
        </div>
      </div>

      {error && <div className="badge badge-red" style={{ marginBottom: 16, padding: '8px 14px', fontSize: 12 }}>Error: {error}</div>}

      {/* === TEMPLATES (collapsible) === */}
      <div className="card" style={{ padding: '0 20px' }}>
        <div className="collapsible-header" onClick={() => setTemplatesOpen(o => !o)} style={{ borderBottom: templatesOpen ? undefined : 'none', padding: '14px 0' }}>
          <span className={`chevron ${templatesOpen ? 'open' : ''}`}>▶</span>
          Templates
          <span style={{ opacity: 0.5, marginLeft: 'auto', fontWeight: 400, textTransform: 'none', letterSpacing: 0 }}>
            {templates.length} available
          </span>
        </div>
        {templatesOpen && (
          <div style={{ padding: '16px 0' }}>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))', gap: 12 }}>
              {templates.map((t) => (
                <div key={t.name} className="card" style={{ padding: 14, margin: 0 }}>
                  <div style={{ fontWeight: 600, fontFamily: 'var(--font-mono)', color: 'var(--green)', marginBottom: 4 }}>
                    {t.name}
                  </div>
                  <div style={{ fontSize: 12, color: 'var(--text-muted)', minHeight: 36, marginBottom: 12, lineHeight: 1.5 }}>
                    {t.description}
                  </div>
                  <button className="btn btn-sm" onClick={() => useTemplate(t.name)} style={{ width: '100%' }}>
                    + Use Template
                  </button>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>

      {/* === FLOWS === */}
      {showForm ? (
        <div className="card">
          <div className="card-title">{editing ? `Edit Flow: ${editing.name}` : 'New Flow'}</div>
          <div className="form-group">
            <label>Name</label>
            <input className="toolbar-input" style={{ width: '100%', minWidth: 0 }} value={form.name || ''} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="user-activity-tracker" />
          </div>
          <div className="form-group">
            <label>Description</label>
            <input className="toolbar-input" style={{ width: '100%', minWidth: 0 }} value={form.description || ''} onChange={(e) => setForm({ ...form, description: e.target.value })} placeholder="What does this flow do?" />
          </div>
          <div className="form-row">
            <div className="form-group">
              <label>Trigger</label>
              <select className="toolbar-input" style={{ width: '100%', minWidth: 0 }} value={form.trigger?.type || 'once'} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, type: e.target.value } as FlowTrigger })}>
                {TRIGGER_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
              </select>
            </div>
            {form.trigger?.type === 'delayed' && (
              <div className="form-group">
                <label>Delay (seconds)</label>
                <input type="number" className="toolbar-input" style={{ width: '100%', minWidth: 0 }} value={form.trigger?.delay_seconds || 10} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, delay_seconds: Number(e.target.value) } as FlowTrigger })} />
              </div>
            )}
            {form.trigger?.type === 'recurring' && (
              <div className="form-group">
                <label>Interval (seconds)</label>
                <input type="number" className="toolbar-input" style={{ width: '100%', minWidth: 0 }} value={form.trigger?.interval_seconds || 300} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, interval_seconds: Number(e.target.value) } as FlowTrigger })} />
              </div>
            )}
          </div>

          <div className="card-title" style={{ marginTop: 8 }}>Steps ({form.steps?.length || 0})</div>
          {(form.steps || []).map((step, idx) => (
            <div key={idx} className="card" style={{ padding: 12, margin: '6px 0', background: 'var(--bg-input)' }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
                <span className="copy-id" style={{ width: 60, textAlign: 'center' }}>{idx + 1}</span>
                <input className="toolbar-input" style={{ flex: 1, minWidth: 0 }} placeholder="step id" value={step.id} onChange={(e) => updateStep(idx, { id: e.target.value })} />
                <select className="toolbar-input" style={{ width: 130, minWidth: 0 }} value={step.type} onChange={(e) => updateStep(idx, { type: e.target.value as FlowStep['type'] })}>
                  {STEP_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <button className="btn btn-sm btn-danger" onClick={() => removeStep(idx)} title="Remove step">×</button>
              </div>
              {step.type === 'command' && (
                <div className="form-row" style={{ gap: 8 }}>
                  <input className="toolbar-input" style={{ flex: 1, minWidth: 0 }} placeholder="command_type (e.g. fs_read)" value={step.command_type || ''} onChange={(e) => updateStep(idx, { command_type: e.target.value })} />
                  <input
                    className="toolbar-input"
                    style={{ flex: 2, minWidth: 0, borderColor: paramsErrors[idx] ? 'var(--red)' : undefined }}
                    placeholder='params JSON (optional), e.g. {"path":"/root"}'
                    defaultValue={step.params ? JSON.stringify(step.params) : ''}
                    onBlur={(e) => {
                      const v = e.target.value.trim()
                      if (!v) { updateStep(idx, { params: undefined }); setParamsErrors(s => { const n = { ...s }; delete n[idx]; return n }); return }
                      try {
                        const parsed = JSON.parse(v)
                        updateStep(idx, { params: parsed })
                        setParamsErrors(s => { const n = { ...s }; delete n[idx]; return n })
                      } catch (err) {
                        setParamsErrors(s => ({ ...s, [idx]: (err as Error).message }))
                      }
                    }}
                  />
                </div>
              )}
              {paramsErrors[idx] && (
                <div style={{ fontSize: 11, color: 'var(--red)', fontFamily: 'var(--font-mono)', marginTop: 4 }}>
                  Invalid JSON: {paramsErrors[idx]}
                </div>
              )}
              {step.type === 'emit' && (
                <input className="toolbar-input" style={{ width: '100%', minWidth: 0 }} placeholder="signal name" value={step.signal || ''} onChange={(e) => updateStep(idx, { signal: e.target.value })} />
              )}
              {step.type === 'wait' && (
                <input type="number" className="toolbar-input" style={{ width: 200 }} placeholder="seconds" value={step.seconds || 0} onChange={(e) => updateStep(idx, { seconds: Number(e.target.value) })} />
              )}
              {step.type === 'branch' && (
                <input className="toolbar-input" style={{ width: '100%', minWidth: 0 }} placeholder="condition expression" value={step.condition || ''} onChange={(e) => updateStep(idx, { condition: e.target.value })} />
              )}
              {step.type === 'compute_diff' && (
                <div className="form-row" style={{ gap: 8 }}>
                  <input className="toolbar-input" style={{ flex: 1, minWidth: 0 }} placeholder="left state key" value={step.left || ''} onChange={(e) => updateStep(idx, { left: e.target.value })} />
                  <input className="toolbar-input" style={{ flex: 1, minWidth: 0 }} placeholder="right state key" value={step.right || ''} onChange={(e) => updateStep(idx, { right: e.target.value })} />
                </div>
              )}
            </div>
          ))}
          <button className="btn btn-sm" onClick={addStep} style={{ marginTop: 8 }}>+ Add Step</button>

          <div style={{ display: 'flex', gap: 8, marginTop: 20 }}>
            <button className="btn btn-primary" onClick={save}>{editing ? 'Save Changes' : 'Create Flow'}</button>
            <button className="btn" onClick={() => setShowForm(false)}>Cancel</button>
          </div>
        </div>
      ) : flows.length === 0 ? (
        <div className="empty-state">
          <h3>No flows yet</h3>
          <p>Create a flow to start composing commands + sensors into scheduled triggers, or use a template above.</p>
          <button className="btn btn-primary" onClick={startCreate} style={{ marginTop: 12 }}>+ Create your first flow</button>
        </div>
      ) : (
        <>
          <div className="toolbar">
            <input
              className="toolbar-input"
              placeholder="Search flows..."
              value={flowFilter}
              onChange={(e) => setFlowFilter(e.target.value)}
              style={{ flex: 1, maxWidth: 360 }}
            />
            <span style={{ color: 'var(--text-muted)', fontSize: 12, fontFamily: 'var(--font-mono)' }}>
              {filteredFlows.length} of {flows.length}
            </span>
          </div>
          <div className="card" style={{ padding: 0, overflow: 'hidden', opacity: loading ? 0.5 : 1, transition: 'opacity 0.15s' }}>
            {loading && (
              <div style={{ padding: '14px', textAlign: 'center', color: 'var(--text-muted)', fontSize: 12, fontFamily: 'var(--font-mono)' }}>
                loading flows…
              </div>
            )}
            <div className="table-container" style={{ overflowX: 'auto' }}>
              <table style={{ tableLayout: 'fixed', minWidth: 920 }}>
                <colgroup>
                  <col style={{ width: '40%' }} />
                  <col style={{ width: '14%' }} />
                  <col style={{ width: '8%' }} />
                  <col style={{ width: '10%' }} />
                  <col style={{ width: '8%', minWidth: 90 }} />
                </colgroup>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Trigger</th>
                    <th>Steps</th>
                    <th>Status</th>
                    <th style={{ textAlign: 'right' }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredFlows.map((f) => (
                    <tr key={f.id}>
                      <td>
                        <div style={{ display: 'flex', alignItems: 'center' }}>
                          <span className={`row-status-bar ${f.enabled ? 'green' : 'gray'}`} />
                          <div>
                            <div style={{ fontWeight: 600 }}>{f.name}</div>
                            {f.description && (
                              <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2, maxWidth: 380 }} className="truncate">
                                {f.description}
                              </div>
                            )}
                            <div style={{ marginTop: 4 }}>
                              <span className="copy-id" onClick={() => copyText(f.id)} title="Click to copy full ID">{shortID(f.id)}</span>
                            </div>
                          </div>
                        </div>
                      </td>
                      <td>
                        <span className={`trigger-badge ${f.trigger.type}`}>
                          {f.trigger.type}{f.trigger.interval_seconds ? ` · ${f.trigger.interval_seconds}s` : f.trigger.delay_seconds ? ` · ${f.trigger.delay_seconds}s` : ''}
                        </span>
                      </td>
                      <td style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }}>
                        {f.steps.length}
                      </td>
                      <td>
                        <StatusBadge status={f.enabled ? 'active' : 'inactive'} />
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <ActionMenu label={`Actions for ${f.name}`}>
                          {(close) => (
                            <>
                              <button className="kebab-item" onClick={() => { close(); startEdit(f) }}>✎ Edit</button>
                              <button className="kebab-item" disabled={busy} onClick={() => { close(); toggleEnabled(f) }}>
                                {f.enabled ? '⏸ Disable' : '▶ Enable'}
                              </button>
                              <button className="kebab-item" disabled={busy || agents.length === 0} onClick={() => { close(); setRunPickerFlow(f) }}>
                                ▶ Run Now…
                              </button>
                              <button className="kebab-item" onClick={() => { close(); setShowRunForFlow(f.id) }}>📊 Show Runs</button>
                              <button className="kebab-item danger" disabled={busy} onClick={() => { close(); remove(f.id) }}>🗑 Delete</button>
                            </>
                          )}
                        </ActionMenu>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}

      {/* === RECENT RUNS === */}
      <div className="page-header-row" style={{ marginTop: 32 }}>
        <div>
          <h2 style={{ fontSize: 16, fontFamily: 'var(--font-mono)' }}>Recent Runs</h2>
          {showRunForFlow && (
            <div style={{ marginTop: 6, fontSize: 12, color: 'var(--text-muted)' }}>
              Filtered to one flow · <button className="btn btn-sm" onClick={() => setShowRunForFlow(null)}>clear filter</button>
            </div>
          )}
        </div>
        <div className="page-meta">
          <span className="meta-item">
            <span className="meta-value" style={{ color: metrics.failed > 0 ? 'var(--red)' : 'var(--green)' }}>
              {metrics.failed}
            </span>
            <span style={{ opacity: 0.6 }}>failed (last {metrics.recent})</span>
          </span>
          <span className="meta-item">
            <span className="meta-value">{metrics.succeeded}</span>
            <span style={{ opacity: 0.6 }}>completed</span>
          </span>
        </div>
      </div>

      {runs.length === 0 ? (
        <div className="empty-state" style={{ padding: 32 }}>
          <h3>No runs yet</h3>
          <p>Flow runs will appear here once you trigger one.</p>
        </div>
      ) : (
        <>
          <div className="toolbar">
            <input
              className="toolbar-input"
              placeholder="Search runs by id, agent, error..."
              value={runFilter}
              onChange={(e) => setRunFilter(e.target.value)}
              style={{ flex: 1, maxWidth: 360 }}
            />
            <span style={{ color: 'var(--text-muted)', fontSize: 12, fontFamily: 'var(--font-mono)' }}>
              showing {filteredRuns.length} of {runs.length}
            </span>
          </div>
          <div className="card" style={{ padding: 0, overflow: 'hidden', opacity: loading ? 0.5 : 1, transition: 'opacity 0.15s' }}>
            {loading && (
              <div style={{ padding: '14px', textAlign: 'center', color: 'var(--text-muted)', fontSize: 12, fontFamily: 'var(--font-mono)' }}>
                loading flows…
              </div>
            )}
            <div className="table-container" style={{ overflowX: 'auto' }}>
              <table style={{ tableLayout: 'fixed', minWidth: 980 }}>
                <colgroup>
                  <col style={{ width: '12%' }} />
                  <col style={{ width: '12%' }} />
                  <col style={{ width: '10%' }} />
                  <col style={{ width: '10%' }} />
                  <col style={{ width: '14%' }} />
                  <col style={{ width: '8%' }} />
                  <col style={{ width: '34%' }} />
                </colgroup>
                <thead>
                  <tr>
                    <th>Run</th>
                    <th>Flow</th>
                    <th>Agent</th>
                    <th>Status</th>
                    <th>Started</th>
                    <th>Duration</th>
                    <th>Error</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredRuns.map((r) => (
                    <tr key={r.id}>
                      <td><span className="copy-id" onClick={() => copyText(r.id)}>{shortID(r.id)}</span></td>
                      <td>
                        <span className="copy-id" onClick={() => copyText(r.flow_id)} title={r.flow_id}>{shortID(r.flow_id)}</span>
                      </td>
                      <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{r.agent_id || '—'}</td>
                      <td>
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                          <span className={`row-status-bar ${statusAccent(r.status)}`} style={{ width: 3, height: 16, marginRight: 0 }} />
                          <StatusBadge status={r.status} />
                        </span>
                      </td>
                      <td style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
                        {(r.started_at || '').replace('T', ' ').slice(0, 19)}
                      </td>
                      <td style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: r.duration_ms ? 'var(--text)' : 'var(--text-dim)' }}>
                        {r.duration_ms != null && r.duration_ms >= 0
                          ? r.duration_ms >= 1000 ? `${(r.duration_ms / 1000).toFixed(1)}s` : `${r.duration_ms}ms`
                          : r.status === 'running' || r.status === 'pending' ? '...' : '—'}
                      </td>
                      <td style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: r.error ? 'var(--red)' : 'var(--text-dim)', maxWidth: 320 }} className="truncate" title={r.error || ''}>
                        {r.error || ''}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}

      {/* === RUN-NOW AGENT PICKER MODAL === */}
      {runPickerFlow && (
        <RunPickerModal
          flow={runPickerFlow}
          agents={agents}
          onPick={(agentID) => { runNow(runPickerFlow, agentID); setRunPickerFlow(null) }}
          onClose={() => setRunPickerFlow(null)}
        />
      )}
    </div>
  )
}

// RunPickerModal — replaces the host-browser prompt() with a proper
// operator UX: list of connected agents, click to run, Escape to close.
function RunPickerModal({ flow, agents, onPick, onClose }: {
  flow: FlowRecord
  agents: AgentRecord[]
  onPick: (agentID: string) => void
  onClose: () => void
}) {
  useEffect(() => {
    const onEsc = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onEsc)
    return () => document.removeEventListener('keydown', onEsc)
  }, [onClose])
  const active = agents.filter(a => a.status === 'active')
  const others = agents.filter(a => a.status !== 'active')
  return (
    <div
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.7)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 2000,
      }}
      onClick={onClose}
    >
      <div
        className="card"
        role="dialog"
        aria-modal="true"
        aria-label={`Run ${flow.name} on agent`}
        style={{ width: 480, maxWidth: '90vw', padding: 24, margin: 0 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="card-title">▶ Run “{flow.name}”</div>
        <p style={{ color: 'var(--text-muted)', fontSize: 13, marginBottom: 14 }}>
          Pick an agent to run this flow against. The flow will execute once with that agent as target.
        </p>
        {active.length > 0 && (
          <>
            <div style={{ fontSize: 11, color: 'var(--green-dim)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6 }}>Active</div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: others.length > 0 ? 12 : 0 }}>
              {active.map(a => (
                <button
                  key={a.agent_id}
                  className="kebab-item"
                  onClick={() => onPick(a.agent_id)}
                  style={{ borderRadius: 4, border: '1px solid var(--border)' }}
                >
                  <span style={{ color: 'var(--green)', fontWeight: 600 }}>● {a.name}</span>
                  <span style={{ marginLeft: 'auto', opacity: 0.6, fontSize: 11, fontFamily: 'var(--font-mono)' }}>{a.version}</span>
                </button>
              ))}
            </div>
          </>
        )}
        {others.length > 0 && (
          <>
            <div style={{ fontSize: 11, color: 'var(--text-dim)', textTransform: 'uppercase', letterSpacing: 1, marginBottom: 6, marginTop: 4 }}>Inactive / stale</div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {others.map(a => (
                <button
                  key={a.agent_id}
                  className="kebab-item"
                  disabled
                  title={`Agent is ${a.status} — cannot run flow`}
                  style={{ borderRadius: 4, border: '1px solid var(--border)', opacity: 0.5, cursor: 'not-allowed' }}
                >
                  <span style={{ color: 'var(--text-dim)' }}>○ {a.name}</span>
                  <span style={{ marginLeft: 'auto', opacity: 0.6, fontSize: 11, fontFamily: 'var(--font-mono)' }}>{a.status}</span>
                </button>
              ))}
            </div>
          </>
        )}
        {agents.length === 0 && (
          <div className="empty-state" style={{ padding: 24 }}>
            <h3>No agents connected</h3>
            <p>Wait for an agent to register, then try again.</p>
          </div>
        )}
        <div style={{ display: 'flex', gap: 8, marginTop: 20, justifyContent: 'flex-end' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
        </div>
      </div>
    </div>
  )
}