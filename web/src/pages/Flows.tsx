import { useState, useEffect } from 'react'
import { api } from '../api/client'
import type { FlowRecord, FlowTrigger, FlowStep, FlowTemplate, FlowRun } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'

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

export default function Flows() {
  const [flows, setFlows] = useState<FlowRecord[]>([])
  const [templates, setTemplates] = useState<FlowTemplate[]>([])
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editing, setEditing] = useState<FlowRecord | null>(null)
  const [form, setForm] = useState<Partial<FlowRecord>>(emptyFlow())

  const load = async () => {
    try {
      const [f, t, r] = await Promise.all([
        api.listFlows(),
        api.listFlowTemplates(),
        api.listFlowRuns(),
      ])
      setFlows(Array.isArray(f) ? f : [])
      setTemplates(Array.isArray(t) ? t : [])
      setRuns(Array.isArray(r) ? r : [])
    } catch (e) {
      setError((e as Error).message)
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
    if (!confirm('Delete this flow?')) return
    try {
      await api.deleteFlow(id)
      await load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const toggleEnabled = async (flow: FlowRecord) => {
    try {
      if (flow.enabled) {
        await api.disableFlow(flow.id)
      } else {
        await api.enableFlow(flow.id)
      }
      await load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const runNow = async (flow: FlowRecord, agentID: string) => {
    if (!agentID) {
      alert('Enter an agent_id to run this flow against')
      return
    }
    try {
      await api.runFlowNow(flow.id, agentID)
      await load()
    } catch (e) {
      setError((e as Error).message)
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

  // Steps editor
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

  return (
    <div style={{ padding: 24, color: '#00ff41', fontFamily: 'monospace' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ margin: 0, textShadow: '0 0 8px #00ff41' }}>Flows</h1>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={startCreate} style={btnPrimary}>+ New Flow</button>
          <button onClick={load} style={btn}>↻ Refresh</button>
        </div>
      </div>

      {error && <div style={{ color: '#ff6b6b', marginBottom: 16 }}>Error: {error}</div>}

      {showForm && (
        <div style={formBox}>
          <h3 style={{ marginTop: 0 }}>{editing ? `Edit: ${editing.name}` : 'New Flow'}</h3>
          <label style={lbl}>Name<input style={inp} value={form.name || ''} onChange={(e) => setForm({ ...form, name: e.target.value })} /></label>
          <label style={lbl}>Description<input style={inp} value={form.description || ''} onChange={(e) => setForm({ ...form, description: e.target.value })} /></label>
          <label style={lbl}>Trigger
            <select style={inp} value={form.trigger?.type || 'once'} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, type: e.target.value } as FlowTrigger })}>
              {TRIGGER_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}
            </select>
          </label>
          {form.trigger?.type === 'delayed' && (
            <label style={lbl}>Delay (seconds)<input type="number" style={inp} value={form.trigger?.delay_seconds || 10} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, delay_seconds: Number(e.target.value) } as FlowTrigger })} /></label>
          )}
          {form.trigger?.type === 'recurring' && (
            <label style={lbl}>Interval (seconds)<input type="number" style={inp} value={form.trigger?.interval_seconds || 300} onChange={(e) => setForm({ ...form, trigger: { ...form.trigger, interval_seconds: Number(e.target.value) } as FlowTrigger })} /></label>
          )}

          <h4>Steps</h4>
          {(form.steps || []).map((step, idx) => (
            <div key={idx} style={stepBox}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <input style={{ ...inp, width: 80 }} value={step.id} onChange={(e) => updateStep(idx, { id: e.target.value })} />
                <select style={inp} value={step.type} onChange={(e) => updateStep(idx, { type: e.target.value })}>
                  {STEP_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <button onClick={() => removeStep(idx)} style={btnDanger}>×</button>
              </div>
              {step.type === 'command' && (
                <input style={inp} placeholder="command_type" value={step.command_type || ''} onChange={(e) => updateStep(idx, { command_type: e.target.value })} />
              )}
              {step.type === 'emit' && (
                <input style={inp} placeholder="signal name" value={step.signal || ''} onChange={(e) => updateStep(idx, { signal: e.target.value })} />
              )}
              {step.type === 'wait' && (
                <input type="number" style={inp} placeholder="seconds" value={step.seconds || 0} onChange={(e) => updateStep(idx, { seconds: Number(e.target.value) })} />
              )}
            </div>
          ))}
          <button onClick={addStep} style={btn}>+ Add Step</button>

          <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
            <button onClick={save} style={btnPrimary}>{editing ? 'Update' : 'Create'}</button>
            <button onClick={() => setShowForm(false)} style={btn}>Cancel</button>
          </div>
        </div>
      )}

      <h2>Templates</h2>
      {templates.length === 0 ? (
        <div style={{ opacity: 0.6, marginBottom: 24 }}>No templates loaded. Place JSON files in the flowtemplates directory.</div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 12, marginBottom: 24 }}>
          {templates.map((t) => (
            <div key={t.name} style={card}>
              <div style={{ fontWeight: 'bold' }}>{t.name}</div>
              <div style={{ fontSize: 12, opacity: 0.7, minHeight: 32 }}>{t.description}</div>
              <button onClick={() => useTemplate(t.name)} style={btn}>Use Template</button>
            </div>
          ))}
        </div>
      )}

      <h2>Flows</h2>
      {flows.length === 0 ? (
        <div style={{ opacity: 0.6 }}>No flows yet. Create one or use a template above.</div>
      ) : (
        <table style={table}>
          <thead>
            <tr>
              <th style={th}>Name</th>
              <th style={th}>Trigger</th>
              <th style={th}>Steps</th>
              <th style={th}>Enabled</th>
              <th style={th}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {[...flows].sort((a, b) => a.name.localeCompare(b.name)).map((f) => (
              <tr key={f.id}>
                <td style={td}><div style={{ fontWeight: 'bold' }}>{f.name}</div><div style={{ fontSize: 11, opacity: 0.6 }}>{f.id.slice(0, 12)}…</div></td>
                <td style={td}>{f.trigger.type}{f.trigger.interval_seconds ? ` (${f.trigger.interval_seconds}s)` : ''}</td>
                <td style={td}>{f.steps.length}</td>
                <td style={td}><StatusBadge status={f.enabled ? 'active' : 'inactive'} /></td>
                <td style={td}>
                  <button onClick={() => startEdit(f)} style={btn}>Edit</button>
                  <button onClick={() => toggleEnabled(f)} style={btn}>{f.enabled ? 'Disable' : 'Enable'}</button>
                  <button onClick={() => { const aid = prompt('Agent ID:'); if (aid) runNow(f, aid); }} style={btn}>Run</button>
                  <button onClick={() => remove(f.id)} style={btnDanger}>Del</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h2 style={{ marginTop: 32 }}>Recent Runs</h2>
      {runs.length === 0 ? (
        <div style={{ opacity: 0.6 }}>No runs yet.</div>
      ) : (
        <table style={table}>
          <thead>
            <tr>
              <th style={th}>Run ID</th>
              <th style={th}>Flow</th>
              <th style={th}>Agent</th>
              <th style={th}>Status</th>
              <th style={th}>Started</th>
              <th style={th}>Error</th>
            </tr>
          </thead>
          <tbody>
            {runs.slice(0, 20).map((r) => (
              <tr key={r.id}>
                <td style={td}><code>{r.id.slice(0, 12)}…</code></td>
                <td style={td}><code>{r.flow_id.slice(0, 12)}…</code></td>
                <td style={td}>{r.agent_id || '—'}</td>
                <td style={td}><StatusBadge status={r.status} /></td>
                <td style={td}>{r.started_at}</td>
                <td style={td}>{r.error || ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

const btn = {
  background: 'transparent',
  border: '1px solid #00ff41',
  color: '#00ff41',
  padding: '6px 12px',
  cursor: 'pointer',
  fontFamily: 'monospace',
  fontSize: 12,
  margin: '0 2px',
} as const

const btnPrimary = { ...btn, background: '#00ff4122', fontWeight: 'bold' as const }
const btnDanger = { ...btn, borderColor: '#ff6b6b', color: '#ff6b6b' }
const lbl = { display: 'block', margin: '8px 0', fontSize: 12 }
const inp = {
  background: '#0a0a0a',
  border: '1px solid #00ff4166',
  color: '#00ff41',
  padding: '6px 8px',
  fontFamily: 'monospace',
  fontSize: 13,
  width: '100%',
  marginTop: 4,
  boxSizing: 'border-box' as const,
} as const
const formBox = { border: '1px solid #00ff41', padding: 16, marginBottom: 24, background: '#0a0a0a' }
const stepBox = { border: '1px solid #00ff4133', padding: 8, margin: '8px 0', display: 'flex', flexDirection: 'column' as const, gap: 4 }
const card = { border: '1px solid #00ff4133', padding: 12, display: 'flex', flexDirection: 'column' as const, gap: 8, background: '#0a0a0a' }
const table = { width: '100%', borderCollapse: 'collapse' as const, fontSize: 13 }
const th = { textAlign: 'left' as const, borderBottom: '1px solid #00ff4144', padding: 8, color: '#00ff41aa' }
const td = { borderBottom: '1px solid #00ff4122', padding: 8, verticalAlign: 'top' as const }
