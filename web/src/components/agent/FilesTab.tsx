import { useState, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { Folder, File, ArrowUp, RefreshCw, ChevronLeft, Download, Upload, Loader } from 'lucide-react'

interface FileEntry { name: string; size: number; is_dir: boolean; mod_time?: string }

export function FilesTab({ agentId, agentOS }: { agentId: string; agentOS?: string }) {
  // Detect the REMOTE agent's OS, not the browser's OS.
  // agentOS comes from the agent record (runtime.GOOS reported by the agent).
  // Falls back to browser detection if agentOS is not provided.
  const isWindows = agentOS ? agentOS === 'windows' : navigator.userAgent.includes('Windows')
  const defaultPath = isWindows ? 'C:\\' : '/'
  const [leftPath, setLeftPath] = useState(defaultPath)
  const [leftEntries, setLeftEntries] = useState<FileEntry[]>([])
  const [rightPath, setRightPath] = useState(defaultPath)
  const [rightEntries, setRightEntries] = useState<FileEntry[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [activePane, setActivePane] = useState<'left' | 'right'>('left')
  const [selected, setSelected] = useState<FileEntry | null>(null)
  const [fileContent, setFileContent] = useState('')
  const [viewingPath, setViewingPath] = useState('')
  const [showViewer, setShowViewer] = useState(false)
  const [transferMsg, setTransferMsg] = useState('')

  const listDir = useCallback(async (dir: string, setPath: (p: string) => void, setEntries: (e: FileEntry[]) => void) => {
    setLoading(true); setError(''); setSelected(null)
    try {
      const res = await api.fsList(agentId, dir)
      const arr = Array.isArray(res) ? res : (res as { entries?: FileEntry[] })?.entries || []
      setEntries(arr.sort((a, b) => { if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1; return a.name.localeCompare(b.name) }))
      setPath(dir)
    } catch (e) { setError((e as Error).message); setEntries([]) }
    finally { setLoading(false) }
  }, [agentId])

  // Auto-detect the agent's current working directory on mount.
  // Runs a quick `pwd` (Linux/macOS) or `cd` (Windows) command to find
  // where the agent process is running, and uses that as the initial path.
  useEffect(() => {
    const detectPath = async () => {
      try {
        const cmd = isWindows ? 'cd' : 'pwd'
        const res = await api.execCmd(agentId, cmd) as { stdout?: string }
        let detected = (res?.stdout || '').trim()
        if (detected) {
          // Windows `cd` returns the path with backslashes; keep as-is.
          // Linux/macOS `pwd` returns the path with forward slashes.
          listDir(detected, setLeftPath, setLeftEntries)
          listDir(detected, setRightPath, setRightEntries)
          return
        }
      } catch {
        // Agent may not support exec or command may fail — fall through to default
      }
      // Fallback: use default path
      listDir(defaultPath, setLeftPath, setLeftEntries)
      listDir(defaultPath, setRightPath, setRightEntries)
    }
    detectPath()
  }, [agentId, isWindows, listDir])

  const joinPath = (base: string, name: string) => {
    const sep = base.includes('/') && !base.includes('\\') ? '/' : (base.endsWith('\\') || base.endsWith('/') ? '' : '\\')
    return base + sep + name
  }

  const goUp = (path: string): string => {
    if (path === 'C:\\' || path === '/' || path === '.') return path
    const sep = path.includes('/') && !path.includes('\\') ? '/' : '\\'
    const parts = path.split(/[\\/]/); parts.pop()
    let p = parts.join(sep)
    if (p && !p.includes(':') && sep === '\\') p += '\\'
    if (!p) p = (path.includes('/') && !path.includes('\\')) ? '/' : 'C:\\'
    return p
  }

  const onFolderClick = (entry: FileEntry, currentPath: string) => {
    if (!entry.is_dir) return
    const newPath = joinPath(currentPath, entry.name)
    if (activePane === 'left') listDir(newPath, setLeftPath, setLeftEntries)
    else listDir(newPath, setRightPath, setRightEntries)
  }

  const onEntryClick = (entry: FileEntry, currentPath: string) => {
    setSelected(entry)
    if (entry.is_dir) {
      const newPath = joinPath(currentPath, entry.name)
      if (activePane === 'left') listDir(newPath, setRightPath, setRightEntries)
      else listDir(newPath, setLeftPath, setLeftEntries)
    }
  }

  const onEntryDoubleClick = (entry: FileEntry, currentPath: string) => {
    if (entry.is_dir) onFolderClick(entry, currentPath)
    else readFile(entry, currentPath)
  }

  const readFile = async (entry: FileEntry, currentPath: string) => {
    setLoading(true); setError('')
    try {
      const fp = joinPath(currentPath, entry.name)
      const res = await api.fsRead(agentId, fp)
      const c = typeof res === 'string' ? res : (res as { content?: string })?.content || JSON.stringify(res, null, 2)
      setFileContent(c); setViewingPath(fp); setShowViewer(true)
    } catch (e) { setError((e as Error).message) } finally { setLoading(false) }
  }

  // Download: pull file from agent to server
  const downloadFile = async (entry: FileEntry, currentPath: string) => {
    if (entry.is_dir) return
    setError(''); setTransferMsg('')
    try {
      const remotePath = joinPath(currentPath, entry.name)
      const localPath = `/tmp/probe-files/${entry.name}`
      const res = await api.createTransfer(agentId, 'download', remotePath, localPath) as { id?: string }
      setTransferMsg(`Download started: ${entry.name} → ${localPath} (ID: ${res?.id?.slice(0, 12) || '?'}) — check Transfers page for progress`)
    } catch (e) { setError((e as Error).message) }
  }

  // Upload: push file from server to agent
  const uploadFile = async (entry: FileEntry, currentPath: string) => {
    if (entry.is_dir) return
    setError(''); setTransferMsg('')
    try {
      // For upload, the local_path is on the server, remote_path is on the agent
      const localPath = `/tmp/probe-files/${entry.name}`
      const remotePath = joinPath(currentPath, entry.name)
      const res = await api.createTransfer(agentId, 'upload', remotePath, localPath) as { id?: string }
      setTransferMsg(`Upload started: ${localPath} → ${remotePath} (ID: ${res?.id?.slice(0, 12) || '?'}) — check Transfers page for progress`)
    } catch (e) { setError((e as Error).message) }
  }

  if (showViewer) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
        <div className="toolbar" style={{ flexShrink: 0 }}>
          <button className="btn btn-sm" onClick={() => setShowViewer(false)}><ChevronLeft size={14} /> Back</button>
          <span className="mono dim">{viewingPath}</span>
        </div>
        <div className="terminal-output" style={{ flex: 1, minHeight: 0 }}>{fileContent}</div>
      </div>
    )
  }

  const renderPane = (side: 'left' | 'right', path: string, entries: FileEntry[]) => {
    const isActive = activePane === side
    const setPath = side === 'left' ? setLeftPath : setRightPath
    const setEntries = side === 'left' ? setLeftEntries : setRightEntries
    return (
      <div
        className={`file-pane ${isActive ? 'active' : ''}`}
        onClick={() => setActivePane(side)}
        style={isActive ? { borderColor: 'var(--green)', boxShadow: '0 0 8px rgba(0,255,65,0.1)' } : {}}
      >
        <div className="file-pane-header">
          <Folder size={14} /> {path}
        </div>
        <div className="file-list">
          {loading && isActive ? <div className="loading">Loading…</div> :
           entries.length === 0 && !error ? <div className="empty-state">Empty directory</div> :
           <div>
             <div className="file-item file-header" style={{ cursor: 'default', fontWeight: 600, fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', borderBottom: '1px solid var(--border)', marginBottom: 4 }}>
               <span className="file-icon" />
               <span className="file-name">Name</span>
               <span className="file-size">Size</span>
               <span className="file-icon" style={{ width: 60 }} />
             </div>
             {entries.map((e, i) => (
              <div
                key={i}
                className={`file-item ${e.is_dir ? 'dir' : ''} ${selected === e && isActive ? 'selected' : ''}`}
                onClick={() => onEntryClick(e, path)}
                onDoubleClick={() => onEntryDoubleClick(e, path)}
              >
                <span className="file-icon">{e.is_dir ? <Folder size={14} /> : <File size={14} />}</span>
                <span className="file-name">{e.name}</span>
                <span className="file-size">{e.is_dir ? '—' : formatSize(e.size)}</span>
                <span className="file-icon" style={{ width: 60, display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                  {!e.is_dir && (
                    <>
                      <button className="btn btn-sm" style={{ padding: '2px 4px', fontSize: 10 }} onClick={(ev) => { ev.stopPropagation(); downloadFile(e, path) }} title="Download from agent"><Download size={12} /></button>
                      <button className="btn btn-sm" style={{ padding: '2px 4px', fontSize: 10 }} onClick={(ev) => { ev.stopPropagation(); uploadFile(e, path) }} title="Upload to agent"><Upload size={12} /></button>
                    </>
                  )}
                </span>
              </div>
            ))}
           </div>}
        </div>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      <div className="toolbar" style={{ flexShrink: 0 }}>
        <button className="btn btn-sm" onClick={() => { listDir(goUp(activePane === 'left' ? leftPath : rightPath), activePane === 'left' ? setLeftPath : setRightPath, activePane === 'left' ? setLeftEntries : setRightEntries) }}><ArrowUp size={14} /> Up</button>
        <button className="btn btn-sm" onClick={() => { if (activePane === 'left') listDir(leftPath, setLeftPath, setLeftEntries); else listDir(rightPath, setRightPath, setRightEntries) }}><RefreshCw size={14} /> Refresh</button>
        <input type="text" value={activePane === 'left' ? leftPath : rightPath} onChange={e => { if (activePane === 'left') setLeftPath(e.target.value); else setRightPath(e.target.value) }} onKeyDown={e => { if (e.key === 'Enter') { const p = activePane === 'left' ? leftPath : rightPath; listDir(p, activePane === 'left' ? setLeftPath : setRightPath, activePane === 'left' ? setLeftEntries : setRightEntries) } }} className="mono" style={{ flex: 1, padding: '5px 10px', border: '1px solid var(--border-glow)', borderRadius: 'var(--radius)', background: 'var(--bg-input)', color: 'var(--green)', fontFamily: 'var(--font-mono)' }} />
      </div>
      {error && <div className="error-msg">{error}</div>}
      {transferMsg && <div className="success-msg">{transferMsg}</div>}
      <div className="file-browser" style={{ flex: 1, minHeight: 0 }}>
        {renderPane('left', leftPath, leftEntries)}
        {renderPane('right', rightPath, rightEntries)}
      </div>
    </div>
  )
}

function formatSize(b: number): string {
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1073741824) return `${(b / 1048576).toFixed(1)} MB`
  return `${(b / 1073741824).toFixed(1)} GB`
}