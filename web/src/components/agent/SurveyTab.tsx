import { useState, useEffect } from 'react'
import { api } from '../../api/client'
import type { SurveyEvent } from '../../api/types'
import { StatusBadge } from '../StatusBadge'

interface Props {
  agentId: string
}

export function SurveyTab({ agentId }: Props) {
  const [events, setEvents] = useState<SurveyEvent[]>([])
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [autoRefresh, setAutoRefresh] = useState(true)

  const load = async () => {
    try {
      const ev = await api.listAgentSurveyEvents(agentId, { limit: 200 })
      setEvents(Array.isArray(ev) ? ev : [])
      setError('')
    } catch (e) {
      setError((e as Error).message)
    }
  }

  useEffect(() => {
    load()
  }, [agentId])

  useEffect(() => {
    if (!autoRefresh) return
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [autoRefresh, agentId])

  const filtered = events.filter((e) => {
    if (!filter) return true
    const f = filter.toLowerCase()
    return (
      e.signal.toLowerCase().includes(f) ||
      e.flow_id.toLowerCase().includes(f) ||
      e.run_id.toLowerCase().includes(f) ||
      JSON.stringify(e.payload).toLowerCase().includes(f)
    )
  })

  return (
    <div style={{ padding: 16, color: '#00ff41', fontFamily: 'monospace' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, textShadow: '0 0 6px #00ff41' }}>Survey Events</h3>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <input
            placeholder="Filter by signal/flow/payload…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{
              background: '#0a0a0a',
              border: '1px solid #00ff4166',
              color: '#00ff41',
              padding: '4px 8px',
              fontFamily: 'monospace',
              fontSize: 12,
              width: 280,
            }}
          />
          <label style={{ fontSize: 12, display: 'flex', alignItems: 'center', gap: 4 }}>
            <input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />
            Auto-refresh
          </label>
          <button onClick={load} style={btn}>↻</button>
        </div>
      </div>

      {error && <div style={{ color: '#ff6b6b', marginBottom: 12 }}>Error: {error}</div>}

      {filtered.length === 0 ? (
        <div style={{ opacity: 0.6, padding: 24, textAlign: 'center' }}>
          {events.length === 0
            ? 'No survey events yet. Run a flow with emit steps to generate events.'
            : 'No events match the current filter.'}
        </div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead>
            <tr>
              <th style={th}>Time</th>
              <th style={th}>Signal</th>
              <th style={th}>Run</th>
              <th style={th}>Payload</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((e) => (
              <tr key={e.id}>
                <td style={td}>{new Date(e.timestamp).toLocaleString()}</td>
                <td style={td}><StatusBadge status={e.signal} /></td>
                <td style={td}><code style={{ fontSize: 11 }}>{e.run_id.slice(0, 12)}…</code></td>
                <td style={td}>
                  <pre style={{
                    margin: 0,
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-all',
                    fontSize: 11,
                    maxHeight: 120,
                    overflow: 'auto',
                    background: '#000a0a',
                    padding: 4,
                    border: '1px solid #00ff4122',
                  }}>
                    {JSON.stringify(e.payload, null, 2)}
                  </pre>
                </td>
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
  padding: '4px 10px',
  cursor: 'pointer',
  fontFamily: 'monospace',
  fontSize: 12,
} as const

const th = { textAlign: 'left' as const, borderBottom: '1px solid #00ff4144', padding: 6, color: '#00ff41aa' }
const td = { borderBottom: '1px solid #00ff4122', padding: 6, verticalAlign: 'top' as const }
