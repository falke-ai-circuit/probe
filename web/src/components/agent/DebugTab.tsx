import { useState } from 'react'
import { api } from '../../api/client'
import { Bug, Link2, Unlink, Cpu, MemoryStick, FileSearch, Map } from 'lucide-react'

interface Module { name: string; base_addr: number; size: number; path?: string }
interface MemRegion { base_address: number; size: number; state: number; protect: number; type: number }

export function DebugTab({ agentId }: { agentId: string }) {
  const [pid, setPid] = useState(''); const [exePath, setExePath] = useState('')
  const [addr, setAddr] = useState(''); const [size, setSize] = useState('256')
  const [output, setOutput] = useState(''); const [hexDump, setHexDump] = useState('')
  const [modules, setModules] = useState<Module[]>([])
  const [error, setError] = useState(''); const [attached, setAttached] = useState(false)
  const [attachedPid, setAttachedPid] = useState<number | null>(null)
  const [debugId, setDebugId] = useState(''); const [baseAddr, setBaseAddr] = useState(0)
  const [memRegion, setMemRegion] = useState<MemRegion | null>(null)

  const attach = async () => {
    setError(''); setOutput('')
    try {
      const res = await api.debugAttach(agentId, parseInt(pid)) as { debug_id?: string; base_addr?: number; name?: string; path?: string }
      setAttached(true); setAttachedPid(parseInt(pid))
      setDebugId(res.debug_id || ''); setBaseAddr(res.base_addr || 0)
      setOutput(`Attached to PID ${pid}${res.name ? ` (${res.name})` : ''}${res.base_addr ? ` base=0x${res.base_addr.toString(16)}` : ''}`)
    } catch (e) { setError((e as Error).message) }
  }

  const attachExe = async () => {
    if (!exePath.trim()) return
    setError(''); setOutput('')
    try {
      const sr = await api.procStart(agentId, exePath) as { pid?: number }
      if (sr.pid) {
        const res = await api.debugAttach(agentId, sr.pid) as { debug_id?: string; base_addr?: number; name?: string }
        setAttached(true); setAttachedPid(sr.pid); setPid(String(sr.pid))
        setDebugId(res.debug_id || ''); setBaseAddr(res.base_addr || 0)
        setOutput(`Started ${exePath} (PID ${sr.pid}) and attached${res.base_addr ? ` base=0x${res.base_addr.toString(16)}` : ''}`)
      } else {
        setOutput(`Started ${exePath} — could not determine PID`)
      }
    } catch (e) { setError((e as Error).message) }
  }

  const detach = async () => {
    setError('')
    try {
      await api.debugDetach(agentId, debugId)
      setAttached(false); setAttachedPid(null); setModules([])
      setHexDump(''); setMemRegion(null); setOutput('Detached')
    } catch (e) { setError((e as Error).message) }
  }

  const readMem = async () => {
    setError('')
    try {
      const readAddr = addr ? parseInt(addr) : baseAddr
      const res = await api.debugReadMem(agentId, debugId, readAddr, parseInt(size)) as { data?: string; hex_data?: string; address?: number }
      if (res.hex_data) { setHexDump(res.hex_data); setOutput('') }
      else { setOutput(JSON.stringify(res, null, 2)); setHexDump('') }
    } catch (e) { setError((e as Error).message) }
  }

  const getModules = async () => {
    setError('')
    try {
      const res = await api.debugModules(agentId, debugId) as { modules?: Module[] }
      setModules(res.modules || [])
    } catch (e) { setError((e as Error).message) }
  }

  const queryMem = async () => {
    setError('')
    try {
      const queryAddr = addr ? parseInt(addr) : baseAddr
      const res = await api.debugMemQuery(agentId, debugId, queryAddr) as { region?: MemRegion }
      setMemRegion(res.region || null)
    } catch (e) { setError((e as Error).message) }
  }

  const stateStr = (s: number) => {
    const states: Record<number, string> = { 0x1000: 'COMMIT', 0x10000: 'FREE', 0x2000: 'RESERVE' }
    return states[s] || `0x${s.toString(16)}`
  }
  const protStr = (p: number) => {
    const parts: string[] = []
    if (p & 0x01) parts.push('NOACCESS')
    if (p & 0x02) parts.push('READONLY')
    if (p & 0x04) parts.push('READWRITE')
    if (p & 0x08) parts.push('WRITECOPY')
    if (p & 0x10) parts.push('EXECUTE')
    if (p & 0x20) parts.push('EXECUTE-READ')
    if (p & 0x40) parts.push('EXECUTE-READWRITE')
    return parts.length ? parts.join('|') : `0x${p.toString(16)}`
  }

  return (
    <div>
      {error && <div className="error-msg">{error}</div>}
      <div className="card">
        <div className="card-title"><Bug size={12} style={{ display: 'inline' }} /> Debug Session</div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
          <span className={`status-dot ${attached ? 'active' : 'inactive'}`} style={{ width: 12, height: 12 }} />
          <span style={{ fontSize: 14, fontWeight: 600 }}>{attached ? `Attached to PID ${attachedPid} (debug_id: ${debugId.slice(0, 12)})` : 'Not attached'}</span>
        </div>
        <div className="form-row" style={{ marginBottom: 12 }}>
          <div className="form-group" style={{ flex: 1 }}><label>Load & Attach to Executable</label><input type="text" value={exePath} onChange={e => setExePath(e.target.value)} placeholder="C:\path\to\program.exe" className="mono" /></div>
          <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><button className="btn btn-primary btn-sm" onClick={attachExe} disabled={attached || !exePath.trim()}><FileSearch size={14} /> Load & Attach</button></div>
        </div>
        <div className="form-row">
          <div className="form-group" style={{ flex: 0, width: 120 }}><label>Target PID</label><input type="number" value={pid} onChange={e => setPid(e.target.value)} placeholder="1234" /></div>
          <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><div className="flex gap-8"><button className="btn btn-primary btn-sm" onClick={attach} disabled={attached}><Link2 size={14} /> Attach</button><button className="btn btn-danger btn-sm" onClick={detach} disabled={!attached}><Unlink size={14} /> Detach</button></div></div>
        </div>
      </div>
      {attached && (
        <>
          <div className="card">
            <div className="card-title"><Cpu size={12} style={{ display: 'inline' }} /> Loaded Modules <button className="btn btn-sm" style={{ marginLeft: 8 }} onClick={getModules}><FileSearch size={14} /> Load</button></div>
            {modules.length > 0 ? (
              <div className="table-container"><table>
                <thead><tr><th>Module</th><th>Base Address</th><th>Size</th><th>Path</th></tr></thead>
                <tbody>{modules.map((m, i) => (
                  <tr key={i} style={{ cursor: 'pointer' }} onClick={() => { setBaseAddr(m.base_addr); setAddr(String(m.base_addr)) }}>
                    <td className="mono">{m.name}</td>
                    <td className="mono green">0x{m.base_addr.toString(16)}</td>
                    <td className="mono dim">{formatSize(m.size)}</td>
                    <td className="mono dim">{m.path || '—'}</td>
                  </tr>
                ))}</tbody>
              </table></div>
            ) : <div className="empty-state" style={{ padding: 16 }}>Click "Load" to enumerate modules. Click a module row to set base address.</div>}
          </div>
          <div className="card">
            <div className="card-title"><MemoryStick size={12} style={{ display: 'inline' }} /> Memory Reader</div>
            <div className="form-row" style={{ marginBottom: 12 }}>
              <div className="form-group" style={{ flex: 1 }}><label>Address (decimal or 0x hex)</label><input type="text" value={addr || String(baseAddr)} onChange={e => setAddr(e.target.value)} placeholder={String(baseAddr)} className="mono" /></div>
              <div className="form-group" style={{ flex: 0, width: 100 }}><label>Size (bytes)</label><input type="number" value={size} onChange={e => setSize(e.target.value)} /></div>
              <div className="form-group" style={{ flex: 0 }}><label>&nbsp;</label><div className="flex gap-8">
                <button className="btn btn-sm" onClick={readMem}><MemoryStick size={14} /> Read</button>
                <button className="btn btn-sm" onClick={queryMem}><Map size={14} /> Query Region</button>
              </div></div>
            </div>
            {memRegion && (
              <div className="card" style={{ marginBottom: 12, background: 'var(--bg-input)' }}>
                <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Memory Region Info</div>
                <div className="mono" style={{ fontSize: 12 }}>
                  Base: 0x{memRegion.base_address.toString(16)} · Size: {formatSize(Number(memRegion.size))} · State: {stateStr(memRegion.state)} · Protect: {protStr(memRegion.protect)}
                </div>
              </div>
            )}
            {hexDump && <div className="hex-dump">{hexDump}</div>}
            {output && <div className="terminal-output">{output}</div>}
          </div>
        </>
      )}
    </div>
  )
}

function formatSize(b: number): string {
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB`
  return `${(b / 1048576).toFixed(1)} MB`
}