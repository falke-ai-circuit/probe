import { useState, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { Folder, File, ArrowUp, RefreshCw, ChevronLeft, Download } from 'lucide-react'

interface FileEntry { name: string; size: number; is_dir: boolean; mod_time?: string }

export function FilesTab({ agentId }: { agentId: string }) {
  const [leftPath, setLeftPath] = useState('C:\\')
  const [leftEntries, setLeftEntries] = useState<FileEntry[]>([])
  const [rightPath, setRightPath] = useState('C:\\')
  const [rightEntries, setRightEntries] = useState<FileEntry[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [activePane, setActivePane] = useState<'left' | 'right'>('left')
  const [selected, setSelected] = useState<FileEntry | null>(null)
  const [fileContent, setFileContent] = useState('')
  const [viewingPath, setViewingPath] = useState('')
  const [showViewer, setShowViewer] = useState(false)

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

  useEffect(() => {
    listDir('C:\\', setLeftPath, setLeftEntries)
    listDir('C:\\', setRightPath, setRightEntries)
  }, [listDir])

  const joinPath = (base: string, name: string) => {
    const sep = base.endsWith('\\') || base.endsWith('/') ? '' : '\\'
    return base + sep + name
  }

  const goUp = (path: string): string => {
    if (path === 'C:\\' || path === '/' || path === '.') return path
    const parts = path.split(/[\\/]/); parts.pop()
    let p = parts.join('\\')
    if (p && !p.includes(':')) p += '\\'
    if (!p) p = 'C:\\'
    return p
  }

  // Click a folder: navigate in the ACTIVE pane
  const onFolderClick = (entry: FileEntry, currentPath: string) => {
    if (!entry.is_dir) return
    const newPath = joinPath(currentPath, entry.name)
    if (activePane === 'left') listDir(newPath, setLeftPath, setLeftEntries)
    else listDir(newPath, setRightPath, setRightEntries)
  }

  // Single click: select + show in opposite pane if folder
  const onEntryClick = (entry: FileEntry, currentPath: string) => {
    setSelected(entry)
    if (entry.is_dir) {
      // Preview folder contents in opposite pane
      const newPath = joinPath(currentPath, entry.name)
      if (activePane === 'left') listDir(newPath, setRightPath, setRightEntries)
      else listDir(newPath, setLeftPath, setLeftEntries)
    }
  }

  // Double click: navigate into folder or open file
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