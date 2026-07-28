import { useState, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { ArrowLeftRight, X, Plus, RefreshCw } from 'lucide-react'

interface Tunnel { id: string; listen_port: number; target_host: string; target_port: number; status: string; created_at?: string }

export function TunnelsTab({ agentId }: { agentId: string }) {
  const [targetHost, setTargetHost] = useState(''); const [targetPort, setTargetPort] = useState('')
  const [listenPort, setListenPort] = useState('')
  const [tunnels, setTunnels] = useState<Tunnel[]>([])
  const [error, setError] = useState(''); const [output, setOutput] = useState('')
  const [loading, setLoading] = useState(false)

  const fetchTunnels = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.tunnelList(agentId) as { tunnels?: Tunnel[] }
      if (res.tunnels) setTunnels(res.tunnels.map(t => ({ ...t, status: 'active' })))
    } catch { /* ignore — might not be supported yet */ }
    setLoading(false)
  }, [agentId])

  useEffect(() => { fetchTunnels() }, [fetchTunnels])

  const openTunnel = async () => {
    setError(''); setOutput('')
    try {
      const res = await api.tunnelOpen(agentId, targetHost, parseInt(targetPort), listenPort ? parseInt(listenPort) : 0) as { tunnel_id?: string; listen_port?: number }
      const newTun: Tunnel = {
        id: res.tunnel_id || `tun-${Date.now()}`,
        listen_port: res.listen_port || 0,
        target_host: targetHost,
        target_port: parseInt(targetPort),
        status: 'active',
        created_at: new Date().toISOString()
      }
      setTunnels(prev => [...prev, newTun])
      setOutput(`Tunnel opened: localhost:${newTun.listen_port} → ${targetHost}:${targetPort}`)
      setTargetHost(''); setTargetPort(''); setListenPort('')
    } catch (e) { setError((e as Error).message) }
  }
  const closeTunnel = async (id: string) => { setError(''); try { await api.tunnelClose(agentId, id); setTunnels(prev => prev.map(t => t.id === id ? { ...t, status: 'closed' } : t)); setOutput(`Tunnel closed`) } catch (e) { setError((e as Error).message) } }
  const removeTunnel = (id: string) => setTunnels(prev => prev.filter(t => t.id !== id))

  return (
    <div>
      {error && <div className="error-msg">{error}</div>}
      {output && <div className="success-msg">{output}</div>}
      <div className="card">
        <div className="card-title"><ArrowLeftRight size={12} style={{ display: 'inline' }} /> Open New Tunnel <button className="btn btn-sm" onClick={fetchTunnels} style={{ marginLeft: 8, opacity: loading ? 0.5 : 1 }}><RefreshCw size={12} /></button></div>
        <div className="form-row">
          <div className="form-group"><label>Listen Port (0=auto)</label><input type="number" value={listenPort} onChange={e => setListenPort(e.target.value)} placeholder="0" /></div>
          <div className="form-group"><label>Target Host</label><input type="text" value={targetHost} onChange={e => setTargetHost(e.target.value)} placeholder="127.0.0.1" /></div>
          <div className="form-group"><label>Target Port</label><input type="number" value={targetPort} onChange={e => setTargetPort(e.target.value)} placeholder="8642" /></div>
          <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><button className="btn btn-primary btn-sm" onClick={openTunnel} disabled={!targetHost || !targetPort}><Plus size={14} /> Open</button></div>
        </div>
      </div>
      <div className="card">
        <div className="card-title">Active Tunnels ({tunnels.filter(t => t.status === 'active').length})</div>
        {tunnels.length === 0 ? <div className="empty-state">No tunnels configured</div> :
          tunnels.map(t => (
            <div key={t.id} className={`tunnel-card ${t.status === 'active' ? 'tunnel-active' : 'tunnel-closed'}`}>
              <span className={`status-dot ${t.status === 'active' ? 'active' : 'inactive'}`} />
              <div style={{ flex: 1 }}>
                <div className="mono" style={{ fontSize: 14 }}>localhost:{t.listen_port} <span className="dim">→</span> {t.target_host}:{t.target_port}</div>
                <div className="dim" style={{ fontSize: 11 }}>ID: {t.id.slice(0, 16)} · {t.status === 'active' ? 'Active' : 'Closed'}{t.created_at && ` · ${new Date(t.created_at).toLocaleString()}`}</div>
              </div>
              {t.status === 'active' ? <button className="btn btn-danger btn-sm" onClick={() => closeTunnel(t.id)}><X size={14} /> Close</button> : <button className="btn btn-sm" onClick={() => removeTunnel(t.id)}>Remove</button>}
            </div>
          ))}
      </div>
    </div>
  )
}