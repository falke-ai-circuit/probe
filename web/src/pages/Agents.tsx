import { useEffect, useState, useMemo } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { AgentRecord } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'
import { Settings2, ExternalLink, Search, Wrench } from 'lucide-react'

const ALL_CAPS = ['exec', 'filesystem', 'process', 'tunnel', 'mitm', 'debug', 'capture', 'input', 'clipboard']

export default function Agents() {
  const [agents, setAgents] = useState<AgentRecord[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [selectedAgent, setSelectedAgent] = useState<AgentRecord | null>(null)
  const [caps, setCaps] = useState<string[]>([])
  const [redeploying, setRedeploying] = useState(false)
  const [redeployMsg, setRedeployMsg] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState('all')

  useEffect(() => {
    const load = async () => { try { const a = await api.listAgents(); setAgents((a || []).sort((x, y) => (x.name || x.agent_id).localeCompare(y.name || y.agent_id))); setError('')} catch (e) { setError((e as Error).message) } finally { setLoading(false) } }
    load(); const interval = setInterval(load, 5000); return () => clearInterval(interval)
  }, [])

  const filteredAgents = useMemo(() => {
    return agents.filter(a => {
      if (statusFilter !== 'all' && a.status !== statusFilter) return false
      if (searchQuery) {
        const q = searchQuery.toLowerCase()
        const name = (a.name || '').toLowerCase()
        const id = a.agent_id.toLowerCase()
        const os = (a.os || '').toLowerCase()
        if (!name.includes(q) && !id.includes(q) && !os.includes(q)) return false
      }
      return true
    })
  }, [agents, searchQuery, statusFilter])

  const openCapEditor = (agent: AgentRecord) => { setSelectedAgent(agent); setCaps(agent.capabilities || []); setRedeployMsg('') }
  const toggleCap = (cap: string) => { setCaps(prev => prev.includes(cap) ? prev.filter(c => c !== cap) : [...prev, cap]) }
  const redeploy = async () => { if (!selectedAgent) return; setRedeploying(true); setRedeployMsg(''); try { const res = await api.redeployAgent(selectedAgent.agent_id, caps); setRedeployMsg(`Build started: ${res.build_id} (status: ${res.status}). Agent will auto-update when build completes.`) } catch (e) { setRedeployMsg(`Error: ${(e as Error).message}`) } finally { setRedeploying(false) } }

  return (
    <div>
      <div className="page-header"><h1>Agents</h1><p>Connected remote agents and their status</p></div>
      {error && <div className="error-msg">{error}</div>}
      {loading ? <div className="loading">Loading agents…</div> :
       agents.length === 0 ? (
        <div className="card">
          <div className="empty-state">
            <p>No agents registered</p>
            <Link to="/builds" className="btn btn-primary btn-sm" style={{ marginTop: 12 }}><Wrench size={14} /> Build an Agent</Link>
          </div>
        </div>
      ) : (
        <div>
        <div className="flex gap-8" style={{ marginBottom: 12, alignItems: 'center' }}>
          <div style={{ position: 'relative', flex: 1, maxWidth: 300 }}>
            <Search size={14} style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)' }} />
            <input
              type="text"
              className="input"
              placeholder="Search by name, ID, or OS…"
              value={searchQuery}
              onChange={e => setSearchQuery(e.target.value)}
              style={{ paddingLeft: 32 }}
            />
          </div>
          <select
            className="input"
            value={statusFilter}
            onChange={e => setStatusFilter(e.target.value)}
            style={{ width: 'auto', minWidth: 120 }}
          >
            <option value="all">All Status</option>
            <option value="active">Active</option>
            <option value="stale">Stale</option>
            <option value="disconnected">Disconnected</option>
          </select>
          <span className="dim" style={{ fontSize: 12 }}>{filteredAgents.length} / {agents.length}</span>
        </div>
        <div className="card"><div className="table-container"><table>
         <thead><tr><th>Name</th><th>OS / Arch</th><th>Status</th><th>Health</th><th>Capabilities</th><th>Connected</th><th>Actions</th></tr></thead>
         <tbody>{filteredAgents.map(a => (
           <tr key={a.agent_id}>
             <td><Link to={`/agents/${a.agent_id}`}>{a.name || a.agent_id}</Link></td>
             <td className="mono">{a.os}/{a.arch}</td>
             <td><StatusBadge status={a.status} /></td>
             <td><div className="flex gap-8" style={{ alignItems: 'center' }}>
               <div style={{ width: '60px', height: '6px', background: 'var(--border)', borderRadius: '3px', overflow: 'hidden' }}>
                 <div style={{ width: `${a.health_score * 100}%`, height: '100%', background: a.health_score > 0.7 ? 'var(--green)' : a.health_score > 0.4 ? 'var(--yellow)' : 'var(--red)' }} />
               </div>
               <span className="mono dim">{(a.health_score * 100).toFixed(0)}%</span>
             </div></td>
             <td><div className="flex gap-8" style={{ flexWrap: 'wrap' }}>
               {(a.capabilities || []).map(c => <span key={c} className="badge badge-green" style={{ fontSize: 10 }}>{c}</span>)}
               {(!a.capabilities || a.capabilities.length === 0) && <span className="dim">—</span>}
             </div></td>
             <td className="dim">{a.connected_at ? new Date(a.connected_at).toLocaleString() : '—'}</td>
             <td><div className="flex gap-8">
               <Link to={`/agents/${a.agent_id}`} className="btn btn-sm"><ExternalLink size={14} /> Manage</Link>
               <button className="btn btn-sm" onClick={() => openCapEditor(a)}><Settings2 size={14} /> Caps</button>
             </div></td>
           </tr>
         ))}</tbody>
       </table></div></div>
       </div>
      )}

      {selectedAgent && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.7)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100 }} onClick={() => setSelectedAgent(null)}>
          <div className="card" style={{ width: 500, maxWidth: '90vw' }} onClick={e => e.stopPropagation()}>
            <div className="card-title">Capabilities — {selectedAgent.name}</div>
            <p className="dim" style={{ fontSize: 12, marginBottom: 14 }}>Toggle capabilities on/off. Click "Redeploy" to rebuild the agent with the new config and push it through the existing connection.</p>
            <div className="checkbox-group" style={{ marginBottom: 16 }}>
              {ALL_CAPS.map(cap => (
                <div key={cap} className={`checkbox-item ${caps.includes(cap) ? 'checked' : ''}`} onClick={() => toggleCap(cap)}>
                  <input type="checkbox" checked={caps.includes(cap)} readOnly /><span className="mono">{cap}</span>
                </div>
              ))}
            </div>
            {redeployMsg && <div className={redeployMsg.startsWith('Error') ? 'error-msg' : 'success-msg'} style={{ marginBottom: 12 }}>{redeployMsg}</div>}
            <div className="flex gap-8">
              <button className="btn btn-primary btn-sm" onClick={redeploy} disabled={redeploying}>{redeploying ? 'Building…' : '↻ Redeploy Agent'}</button>
              <button className="btn btn-sm" onClick={() => setSelectedAgent(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}