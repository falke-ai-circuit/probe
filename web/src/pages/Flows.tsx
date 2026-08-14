import { useState, useEffect, useRef, useMemo } from 'react'
import { api } from '../api/client'
import type { FlowRecord, FlowTrigger, FlowStep, FlowRun, AgentRecord } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'

const STEP_TYPES = ['command', 'wait', 'branch', 'compute_diff', 'classify', 'emit', 'loop']
const STEP_COLORS: Record<string, string> = {
  command: 'var(--cyan)',
  wait: 'var(--yellow)',
  branch: 'var(--purple)',
  compute_diff: 'var(--blue)',
  classify: 'var(--green)',
  emit: 'var(--orange)',
  loop: 'var(--red)',
}
const NODE_W = 220
const NODE_H = 96

interface NodePos { x: number; y: number }
interface Edge { from: string; to: string; label?: string }

const newStepId = () => 's' + Math.random().toString(36).slice(2, 7)
const emptyStep = (): FlowStep => ({ id: newStepId(), type: 'command', command_type: 'sysinfo' })

function defaultLayout(steps: FlowStep[]): Record<string, NodePos> {
  const out: Record<string, NodePos> = {}
  steps.forEach((s, i) => { out[s.id] = { x: 40, y: 40 + i * 140 } })
  return out
}

export default function Flows() {
  const [flows, setFlows] = useState<FlowRecord[]>([])
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [selectedFlow, setSelectedFlow] = useState<FlowRecord | null>(null)
  const [positions, setPositions] = useState<Record<string, NodePos>>({})
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [drag, setDrag] = useState<{ id: string; startX: number; startY: number; origX: number; origY: number } | null>(null)
  const [liveRun, setLiveRun] = useState<FlowRun | null>(null)
  const canvasRef = useRef<HTMLDivElement>(null)

  const load = async () => {
    try {
      const [f, r, a] = await Promise.all([
        api.listFlows(),
        api.listFlowRuns(),
        api.listAgents().catch(() => []),
      ])
      setFlows(Array.isArray(f) ? f : [])
      setRuns(Array.isArray(r) ? r : [])
      setAgents(Array.isArray(a) ? a : [])
    } catch (e) {
      setError((e as Error).message)
    }
  }
  useEffect(() => { load() }, [])

  const selectFlow = (f: FlowRecord) => {
    setSelectedFlow(f)
    setPositions(defaultLayout(f.steps))
    setLiveRun(null)
  }

  // Edges: next links + branch if_true/if_false.
  const edges = useMemo<Edge[]>(() => {
    if (!selectedFlow) return []
    const out: Edge[] = []
    for (const s of selectedFlow.steps) {
      if (s.next) out.push({ from: s.id, to: s.next })
      if (s.type === 'branch') {
        const b = s as unknown as { if_true?: string; if_false?: string }
        if (b.if_true) out.push({ from: s.id, to: b.if_true, label: 'true' })
        if (b.if_false) out.push({ from: s.id, to: b.if_false, label: 'false' })
      }
    }
    return out
  }, [selectedFlow])

  const posOf = (id: string): NodePos => positions[id] || defaultLayout(selectedFlow?.steps || [])[id] || { x: 40, y: 40 }
  const statusFor = (id: string): 'running' | 'ok' | 'error' | 'idle' => {
    const s = liveRun?.node_status?.[id]
    if (s === 'running' || liveRun?.active_node === id) return 'running'
    if (s === 'ok') return 'ok'
    if (s === 'error') return 'error'
    return 'idle'
  }
  const centerOf = (id: string): NodePos => {
    const p = posOf(id)
    return { x: p.x + NODE_W / 2, y: p.y + NODE_H / 2 }
  }

  const createFlow = async () => {
    setError('')
    const name = prompt('Flow name:', 'user-activity-tracker')
    if (!name) return
    try {
      const flow = await api.createFlow({ name, trigger: { type: 'once' } as FlowTrigger, steps: [emptyStep()] })
      await load()
      const f = flows.find(x => x.id === (flow as FlowRecord).id) || (flow as FlowRecord)
      selectFlow(f)
    } catch (e) { setError((e as Error).message) }
  }

  const addStep = () => {
    if (!selectedFlow) return
    const steps = [...selectedFlow.steps, emptyStep()]
    setSelectedFlow({ ...selectedFlow, steps })
    const last = steps[steps.length - 1]
    setPositions(p => ({ ...p, [last.id]: { x: 40, y: 40 + (steps.length - 1) * 140 } }))
  }

  const updateStep = (idx: number, patch: Partial<FlowStep>) => {
    if (!selectedFlow) return
    const steps = selectedFlow.steps.map((s, i) => (i === idx ? { ...s, ...patch } : s))
    setSelectedFlow({ ...selectedFlow, steps })
  }

  const removeStep = (idx: number) => {
    if (!selectedFlow) return
    const steps = selectedFlow.steps.filter((_, i) => i !== idx)
    setSelectedFlow({ ...selectedFlow, steps: steps.length ? steps : [emptyStep()] })
  }

  const save = async () => {
    if (!selectedFlow) return
    setError('')
    setBusy(true)
    try {
      await api.updateFlow(selectedFlow.id, {
        name: selectedFlow.name,
        description: selectedFlow.description,
        trigger: selectedFlow.trigger,
        steps: selectedFlow.steps,
      })
      await load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const runNow = async () => {
    if (!selectedFlow || busy || agents.length === 0) return
    const agentID = agents[0].agent_id
    setBusy(true)
    try {
      const run = await api.runFlowNow(selectedFlow.id, agentID)
      setLiveRun(run as FlowRun)
      pollRun((run as FlowRun).id)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const pollRun = async (id: string) => {
    for (let i = 0; i < 60; i++) {
      try {
        const rs = await api.listFlowRuns(selectedFlow?.id)
        const list = Array.isArray(rs) ? rs : []
        const run = list.find(r => r.id === id)
        if (run) setLiveRun(run)
        if (run && ['completed', 'failed'].includes(run.status)) return
      } catch { /* transient */ }
      await new Promise(res => setTimeout(res, 2000))
    }
  }

  const onPointerDown = (e: React.PointerEvent, id: string) => {
    const p = posOf(id)
    setDrag({ id, startX: e.clientX, startY: e.clientY, origX: p.x, origY: p.y })
  }
  const onPointerMove = (e: React.PointerEvent) => {
    if (!drag) return
    const dx = e.clientX - drag.startX
    const dy = e.clientY - drag.startY
    setPositions(p => ({ ...p, [drag.id]: { x: drag.origX + dx, y: drag.origY + dy } }))
  }
  const onPointerUp = () => setDrag(null)

  const latestRun = useMemo(() => {
    if (!selectedFlow) return null
    return runs.filter(r => r.flow_id === selectedFlow.id).sort((a, b) => (b.started_at || '').localeCompare(a.started_at || ''))[0] || liveRun
  }, [runs, selectedFlow, liveRun])

  const runAccent = latestRun?.status === 'running' ? 'var(--cyan)' : latestRun?.status === 'completed' ? 'var(--green)' : latestRun?.status === 'failed' ? 'var(--red)' : 'var(--border)'

  return (
    <div>
      <div className="page-header-row">
        <div style={{ flex: 1, minWidth: 280 }}>
          <h1>Flows</h1>
          <div className="page-subtitle">
            Compose server-side workflows from commands + sensors. Drag nodes to arrange, wire them with <span style={{ fontFamily: 'var(--font-mono)' }}>next</span>.
          </div>
        </div>
        <div className="page-actions">
          <button className="btn btn-primary" onClick={createFlow}><span style={{ fontSize: 16 }}>+</span> New Flow</button>
          <button className="btn" onClick={load}>↻ Refresh</button>
        </div>
      </div>

      {error && <div className="badge badge-red" style={{ marginBottom: 16, padding: '8px 14px', fontSize: 12 }}>Error: {error}</div>}

      <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
        {/* Flow list sidebar */}
        <div className="card" style={{ width: 240, flexShrink: 0, padding: 12 }}>
          <div className="card-title" style={{ fontSize: 13 }}>Flows ({flows.length})</div>
          {flows.length === 0 && <div style={{ fontSize: 12, color: 'var(--text-muted)', padding: '8px 0' }}>No flows yet.</div>}
          {flows.map(f => (
            <button
              key={f.id}
              className="btn btn-sm"
              onClick={() => selectFlow(f)}
              style={{
                width: '100%', justifyContent: 'flex-start', marginBottom: 6,
                background: selectedFlow?.id === f.id ? 'var(--bg-input)' : undefined,
                borderColor: selectedFlow?.id === f.id ? 'var(--cyan)' : undefined,
              }}
            >
              <span style={{ display: 'flex', alignItems: 'center', gap: 8, overflow: 'hidden' }}>
                <span className="row-status-bar" style={{ background: f.enabled ? 'var(--green)' : 'var(--border)' }} />
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{f.name}</span>
              </span>
            </button>
          ))}
        </div>

        {/* Canvas */}
        <div className="card" style={{ flex: 1, padding: 0, overflow: 'hidden', minHeight: 560 }}>
          {!selectedFlow ? (
            <div className="empty-state" style={{ minHeight: 560 }}>
              <h3>Select a flow</h3>
              <p>Pick a flow from the left, or create a new one, to open its node graph.</p>
            </div>
          ) : (
            <>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '12px 16px', borderBottom: '1px solid var(--border)' }}>
                <input className="toolbar-input" style={{ flex: 1, minWidth: 0, fontWeight: 600 }} value={selectedFlow.name || ''} onChange={e => setSelectedFlow({ ...selectedFlow, name: e.target.value })} placeholder="flow name" />
                <select className="toolbar-input" style={{ width: 120 }} value={selectedFlow.trigger?.type || 'once'} onChange={e => setSelectedFlow({ ...selectedFlow, trigger: { ...selectedFlow.trigger, type: e.target.value } as FlowTrigger })}>
                  <option value="once">once</option>
                  <option value="delayed">delayed</option>
                  <option value="recurring">recurring</option>
                </select>
                {selectedFlow.trigger?.type === 'recurring' && (
                  <input type="number" className="toolbar-input" style={{ width: 90 }} placeholder="interval s" value={selectedFlow.trigger?.interval_seconds || 300} onChange={e => setSelectedFlow({ ...selectedFlow, trigger: { ...selectedFlow.trigger, interval_seconds: Number(e.target.value) } as FlowTrigger })} />
                )}
                <button className="btn btn-sm" onClick={addStep}>+ Step</button>
                <button className="btn btn-sm btn-primary" onClick={save} disabled={busy}>Save</button>
                <button className="btn btn-sm" onClick={runNow} disabled={busy || agents.length === 0}>▶ Run</button>
              </div>

              {latestRun && (
                <div style={{ padding: '8px 16px', fontSize: 12, fontFamily: 'var(--font-mono)', color: runAccent, borderBottom: '1px solid var(--border)' }}>
                  {latestRun.status === 'running' && <span>● running {latestRun.id?.slice(-8)}</span>}
                  {latestRun.status === 'completed' && <span>✓ completed {latestRun.id?.slice(-8)}</span>}
                  {latestRun.status === 'failed' && <span>✗ failed {latestRun.id?.slice(-8)}</span>}
                </div>
              )}

              <div
                ref={canvasRef}
                onPointerMove={onPointerMove}
                onPointerUp={onPointerUp}
                onPointerLeave={onPointerUp}
                style={{
                  position: 'relative', height: 520, overflow: 'auto', cursor: drag ? 'grabbing' : 'default',
                  background: 'radial-gradient(circle, var(--border) 1px, transparent 1px) 0 0 / 24px 24px',
                }}
              >
                {/* SVG edges */}
                <svg style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', pointerEvents: 'none' }}>
                  {edges.map((e, i) => {
                    const a = centerOf(e.from)
                    const b = centerOf(e.to)
                    const mx = (a.x + b.x) / 2
                    return (
                      <g key={i}>
                        <path d={`M ${a.x} ${a.y} C ${mx} ${a.y}, ${mx} ${b.y}, ${b.x} ${b.y}`} fill="none" stroke="var(--cyan)" strokeWidth={2} opacity={0.7} />
                        {e.label && <text x={mx} y={(a.y + b.y) / 2 - 4} fill="var(--cyan)" fontSize={10} fontFamily="var(--font-mono)">{e.label}</text>}
                      </g>
                    )
                  })}
                </svg>

                {/* Node cards */}
                {selectedFlow.steps.map((step, idx) => {
                  const p = posOf(step.id)
                  const color = STEP_COLORS[step.type] || 'var(--cyan)'
                  const st = statusFor(step.id)
                  const borderColor = st === 'running' ? 'var(--cyan)' : st === 'ok' ? 'var(--green)' : st === 'error' ? 'var(--red)' : color
                  const glow = st === 'running' ? '0 0 14px rgba(0,229,255,0.5)' : 'none'
                  return (
                    <div
                      key={step.id}
                      onPointerDown={e => onPointerDown(e, step.id)}
                      style={{
                        position: 'absolute', left: p.x, top: p.y, width: NODE_W, minHeight: NODE_H,
                        background: 'var(--bg-input)', border: `2px solid ${borderColor}`, borderRadius: 8, padding: 10,
                        boxShadow: glow || '0 0 12px rgba(0,0,0,0.3)', cursor: 'grab', userSelect: 'none',
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                        <span className="copy-id" style={{ width: 22, textAlign: 'center' }}>{idx + 1}</span>
                        <span style={{ fontSize: 12, fontWeight: 700, fontFamily: 'var(--font-mono)', color }}>{step.type}</span>
                        <span style={{ marginLeft: 'auto', fontSize: 10, color: 'var(--text-muted)' }}>{step.id}</span>
                        <button className="btn btn-sm btn-danger" style={{ padding: '0 6px' }} onClick={() => removeStep(idx)}>×</button>
                      </div>
                      <select className="toolbar-input" style={{ width: '100%', marginBottom: 6 }} value={step.type} onChange={e => updateStep(idx, { type: e.target.value as FlowStep['type'] })}>
                        {STEP_TYPES.map(t => <option key={t} value={t}>{t}</option>)}
                      </select>
                      {step.type === 'command' && (
                        <input className="toolbar-input" style={{ width: '100%' }} placeholder="command_type (e.g. sensor_read)" value={step.command_type || ''} onChange={e => updateStep(idx, { command_type: e.target.value })} />
                      )}
                      {step.type === 'wait' && (
                        <input type="number" className="toolbar-input" style={{ width: '100%' }} placeholder="seconds" value={step.seconds || 0} onChange={e => updateStep(idx, { seconds: Number(e.target.value) })} />
                      )}
                      {step.type === 'emit' && (
                        <input className="toolbar-input" style={{ width: '100%' }} placeholder="signal" value={(step as unknown as { signal?: string }).signal || ''} onChange={e => updateStep(idx, { signal: e.target.value } as Partial<FlowStep>)} />
                      )}
                      {step.type === 'loop' && (
                        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 6 }}>
                          <input type="number" className="toolbar-input" style={{ width: '100%' }} placeholder="interval seconds" value={step.interval_seconds || 0} onChange={e => updateStep(idx, { interval_seconds: Number(e.target.value) })} />
                          <input type="number" className="toolbar-input" style={{ width: '100%' }} placeholder="max iterations (0 = until stop)" value={step.max_iterations || 0} onChange={e => updateStep(idx, { max_iterations: Number(e.target.value) })} />
                          <input className="toolbar-input" style={{ width: '100%' }} placeholder="stop condition ({{state.x}} == 1)" value={step.stop_condition || ''} onChange={e => updateStep(idx, { stop_condition: e.target.value })} />
                          <div style={{ fontSize: 10, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>body: {step.body?.length || 0} steps</div>
                        </div>
                      )}
                      <input className="toolbar-input" style={{ width: '100%', marginTop: 6 }} placeholder="next step id (leave blank for auto)" value={step.next || ''} onChange={e => updateStep(idx, { next: e.target.value })} />
                    </div>
                  )
                })}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
