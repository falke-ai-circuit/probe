import { useState, useEffect, useRef } from 'react'
import { api } from '../../api/client'
import { Network, Play, Square, Activity, Eye, EyeOff, Radio, StopCircle } from 'lucide-react'

interface MITMSession { id: string; listenAddr: string; targetAddr: string; logPath: string; active: boolean }
interface SniffSession { id: string; targetHost: string; targetPort: number; active: boolean }

export function MITMTab({ agentId }: { agentId: string }) {
  const [listenAddr, setListenAddr] = useState('0.0.0.0:0'); const [targetAddr, setTargetAddr] = useState('')
  const [logPath, setLogPath] = useState(''); const [reuseAddr, setReuseAddr] = useState(false)
  const [sessions, setSessions] = useState<MITMSession[]>([])
  const [error, setError] = useState('')
  const [activeTraffic, setActiveTraffic] = useState<string | null>(null)
  const [trafficContent, setTrafficContent] = useState('')
  const [showTraffic, setShowTraffic] = useState(true)
  const trafficRef = useRef<ReturnType<typeof setInterval>>()

  // Sniff state
  const [sniffHost, setSniffHost] = useState(''); const [sniffPort, setSniffPort] = useState('')
  const [sniffDuration, setSniffDuration] = useState('0')
  const [sniffSessions, setSniffSessions] = useState<SniffSession[]>([])

  const start = async () => {
    setError('')
    try {
      const res = await api.mitmStart(agentId, listenAddr, targetAddr, logPath, reuseAddr) as { mitm_id?: string; listen_addr?: string }
      const mitmId = res.mitm_id || `mitm-${Date.now()}`
      setSessions(prev => [...prev, { id: mitmId, listenAddr: res.listen_addr || listenAddr, targetAddr, logPath, active: true }])
      setTargetAddr(''); setLogPath('')
    } catch (e) { setError((e as Error).message) }
  }

  const stop = async (id: string) => {
    setError('')
    try {
      await api.mitmStop(agentId, id)
      setSessions(prev => prev.map(s => s.id === id ? { ...s, active: false } : s))
      if (activeTraffic === id) { setActiveTraffic(null); if (trafficRef.current) clearInterval(trafficRef.current) }
    } catch (e) { setError((e as Error).message) }
  }

  const viewTraffic = async (id: string) => {
    if (activeTraffic === id) { setActiveTraffic(null); if (trafficRef.current) clearInterval(trafficRef.current); return }
    setActiveTraffic(id)
    const fetchTraffic = async () => {
      try {
        const res = await api.mitmTraffic(agentId, id)
        setTrafficContent(typeof res === 'string' ? res : JSON.stringify(res, null, 2))
      } catch (e) { setTrafficContent(`Error: ${(e as Error).message}`) }
    }
    fetchTraffic(); trafficRef.current = setInterval(fetchTraffic, 2000)
  }

  // Sniff handlers
  const startSniff = async () => {
    setError('')
    try {
      const res = await api.sniffStart(agentId, sniffHost, parseInt(sniffPort), parseInt(sniffDuration) || 0) as { sniff_id?: string }
      const sniffId = res.sniff_id || `sniff-${Date.now()}`
      setSniffSessions(prev => [...prev, { id: sniffId, targetHost: sniffHost, targetPort: parseInt(sniffPort), active: true }])
      setSniffHost(''); setSniffPort('')
    } catch (e) { setError((e as Error).message) }
  }

  const stopSniff = async (id: string) => {
    setError('')
    try {
      await api.sniffStop(agentId, id)
      setSniffSessions(prev => prev.map(s => s.id === id ? { ...s, active: false } : s))
    } catch (e) { setError((e as Error).message) }
  }

  useEffect(() => { return () => { if (trafficRef.current) clearInterval(trafficRef.current) } }, [])

  return (
    <div>
      {error && <div className="error-msg">{error}</div>}

      {/* MITM Section */}
      <div className="card">
        <div className="card-title"><Network size={12} style={{ display: 'inline' }} /> Start MITM Proxy</div>
        <div className="form-row">
          <div className="form-group"><label>Listen Address</label><input type="text" value={listenAddr} onChange={e => setListenAddr(e.target.value)} placeholder="0.0.0.0:0 (auto)" className="mono" /></div>
          <div className="form-group"><label>Target Address</label><input type="text" value={targetAddr} onChange={e => setTargetAddr(e.target.value)} placeholder="127.0.0.1:1516" className="mono" /></div>
        </div>
        <div className="form-row">
          <div className="form-group"><label>Log Path (optional)</label><input type="text" value={logPath} onChange={e => setLogPath(e.target.value)} placeholder="C:\temp\mitm.log" className="mono" /></div>
          <div className="form-group" style={{ flex: 0, minWidth: 120 }}><label>SO_REUSEADDR</label><label className="checkbox-label"><input type="checkbox" checked={reuseAddr} onChange={e => setReuseAddr(e.target.checked)} /> Enable</label></div>
          <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><button className="btn btn-primary btn-sm" onClick={start} disabled={!targetAddr}><Play size={14} /> Start</button></div>
        </div>
      </div>

      <div className="card">
        <div className="card-title">MITM Sessions ({sessions.filter(s => s.active).length} active)</div>
        {sessions.length === 0 ? <div className="empty-state">No MITM sessions</div> :
          sessions.map(s => (
            <div key={s.id} className="mitm-session" style={{ borderLeftColor: s.active ? 'var(--green)' : 'var(--text-dim)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
                <span className={`status-dot ${s.active ? 'active' : 'inactive'}`} />
                <span className="mono" style={{ fontSize: 14 }}>{s.listenAddr} → {s.targetAddr}</span>
                <span style={{ flex: 1 }} />
                {s.active && <button className={`btn btn-sm ${activeTraffic === s.id ? 'btn-primary' : ''}`} onClick={() => viewTraffic(s.id)}><Activity size={14} /> {activeTraffic === s.id ? 'Stop' : 'Traffic'}</button>}
                {s.active ? <button className="btn btn-danger btn-sm" onClick={() => stop(s.id)}><Square size={14} /> Stop</button> : <button className="btn btn-sm" onClick={() => setSessions(prev => prev.filter(x => x.id !== s.id))}>Remove</button>}
              </div>
              <div className="dim" style={{ fontSize: 11 }}>ID: {s.id.slice(0, 16)} · {s.active ? 'Active' : 'Stopped'}</div>
            </div>
          ))}
      </div>

      {activeTraffic && (
        <div className="card">
          <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Activity size={12} /> Live Traffic
            <button className="btn btn-sm" onClick={() => setShowTraffic(!showTraffic)}>{showTraffic ? <EyeOff size={12} /> : <Eye size={12} />}</button>
          </div>
          {showTraffic && <div className="traffic-viewer">{trafficContent || 'Waiting for traffic…'}</div>}
        </div>
      )}

      {/* Sniff Section */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="card-title"><Radio size={12} style={{ display: 'inline' }} /> Traffic Sniffer</div>
        <div className="form-row">
          <div className="form-group"><label>Target Host</label><input type="text" value={sniffHost} onChange={e => setSniffHost(e.target.value)} placeholder="10.0.0.5" /></div>
          <div className="form-group" style={{ flex: 0, width: 100 }}><label>Target Port</label><input type="number" value={sniffPort} onChange={e => setSniffPort(e.target.value)} placeholder="80" /></div>
          <div className="form-group" style={{ flex: 0, width: 100 }}><label>Duration (s)</label><input type="number" value={sniffDuration} onChange={e => setSniffDuration(e.target.value)} placeholder="0 = until stopped" /></div>
          <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><button className="btn btn-primary btn-sm" onClick={startSniff} disabled={!sniffHost || !sniffPort}><Radio size={14} /> Start Sniff</button></div>
        </div>
      </div>

      <div className="card">
        <div className="card-title">Sniff Sessions ({sniffSessions.filter(s => s.active).length} active)</div>
        {sniffSessions.length === 0 ? <div className="empty-state">No sniff sessions</div> :
          sniffSessions.map(s => (
            <div key={s.id} className="mitm-session" style={{ borderLeftColor: s.active ? 'var(--green)' : 'var(--text-dim)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <span className={`status-dot ${s.active ? 'active' : 'inactive'}`} />
                <span className="mono" style={{ fontSize: 14 }}>{s.targetHost}:{s.targetPort}</span>
                <span style={{ flex: 1 }} />
                {s.active && <button className="btn btn-danger btn-sm" onClick={() => stopSniff(s.id)}><StopCircle size={14} /> Stop</button>}
                {!s.active && <button className="btn btn-sm" onClick={() => setSniffSessions(prev => prev.filter(x => x.id !== s.id))}>Remove</button>}
              </div>
              <div className="dim" style={{ fontSize: 11 }}>ID: {s.id.slice(0, 16)} · {s.active ? 'Active' : 'Stopped'}</div>
            </div>
          ))}
      </div>
    </div>
  )
}