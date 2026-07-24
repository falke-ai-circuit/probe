import { useState, useRef, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { Terminal as TerminalIcon, Play, Trash2 } from 'lucide-react'

interface HistoryEntry { cmd: string; output: string }

export function TerminalTab({ agentId }: { agentId: string }) {
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [cmd, setCmd] = useState('')
  const [running, setRunning] = useState(false)
  const [cmdHistory, setCmdHistory] = useState<string[]>([])
  const [histIdx, setHistIdx] = useState(-1)
  const outputRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const scrollDown = useCallback(() => {
    setTimeout(() => { if (outputRef.current) outputRef.current.scrollTop = outputRef.current.scrollHeight }, 50)
  }, [])

  const exec = async (command: string) => {
    if (!command.trim() || running) return
    setRunning(true); setCmdHistory(prev => [...prev, command]); setHistIdx(-1)
    try {
      const res = await api.execCmd(agentId, command)
      const text = typeof res === 'string' ? res : (res as { output?: string })?.output || (res as { stdout?: string })?.stdout || JSON.stringify(res, null, 2)
      setHistory(prev => [...prev, { cmd: command, output: text }])
    } catch (e) { setHistory(prev => [...prev, { cmd: command, output: `Error: ${(e as Error).message}` }]) }
    finally { setRunning(false); setCmd(''); scrollDown() }
  }

  const handleKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') exec(cmd)
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (cmdHistory.length === 0) return; const ni = histIdx === -1 ? cmdHistory.length - 1 : Math.max(0, histIdx - 1); setHistIdx(ni); setCmd(cmdHistory[ni]) }
    else if (e.key === 'ArrowDown') { e.preventDefault(); if (histIdx === -1) return; const ni = histIdx + 1; if (ni >= cmdHistory.length) { setHistIdx(-1); setCmd('') } else { setHistIdx(ni); setCmd(cmdHistory[ni]) } }
    else if (e.key === 'l' && e.ctrlKey) { e.preventDefault(); setHistory([]) }
  }

  useEffect(() => { inputRef.current?.focus() }, [])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      <div className="toolbar" style={{ flexShrink: 0 }}>
        <button className="btn btn-sm" onClick={() => setHistory([])}><Trash2 size={14} /> Clear</button>
        <span className="dim" style={{ fontSize: 11, marginLeft: 8 }}>↑↓ history · Ctrl+L clear</span>
      </div>
      <div className="terminal-output" ref={outputRef} onClick={() => inputRef.current?.focus()}>
        {history.length === 0 ? <span className="dim"><TerminalIcon size={12} style={{ display: 'inline', verticalAlign: 'middle' }} /> PROBE Terminal ready. Type a command and press Enter.</span> :
          history.map((h, i) => (<div key={i}><span style={{ color: '#666' }}>$ </span><span style={{ color: '#fff' }}>{h.cmd}</span>{'\n'}{h.output}{'\n'}</div>))}
        {running && <span className="dim">Executing…</span>}
      </div>
      <div className="terminal-input-row" style={{ flexShrink: 0 }}>
        <span className="prompt">$</span>
        <input ref={inputRef} type="text" value={cmd} onChange={e => setCmd(e.target.value)} onKeyDown={handleKey} placeholder="Enter command…" disabled={running} autoFocus />
        <button className="btn btn-primary btn-sm" onClick={() => exec(cmd)} disabled={running || !cmd.trim()}><Play size={14} /> Run</button>
      </div>
    </div>
  )
}