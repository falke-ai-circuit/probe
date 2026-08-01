import { useState, useEffect, useCallback } from 'react'
import { api } from '../api/client'
import type { FileTransfer } from '../api/types'
import { Upload, Download, Pause, Play, CheckCircle, XCircle, Loader, RefreshCw, FileText, Ban } from 'lucide-react'

export default function Transfers() {
  const [transfers, setTransfers] = useState<FileTransfer[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('all')

  const loadTransfers = useCallback(async () => {
    try {
      const t = await api.listTransfers()
      setTransfers(t || [])
      setError('')
    } catch (e) { setError((e as Error).message) }
    finally { setLoading(false) }
  }, [])

  useEffect(() => {
    loadTransfers()
    const interval = setInterval(loadTransfers, 3000)
    return () => clearInterval(interval)
  }, [loadTransfers])

  const handlePause = async (id: string) => {
    try { await api.pauseTransfer(id); loadTransfers() }
    catch (e) { setError((e as Error).message) }
  }

  const handleResume = async (id: string) => {
    try { await api.resumeTransfer(id); loadTransfers() }
    catch (e) { setError((e as Error).message) }
  }

  const handleCancel = async (id: string) => {
    try { await api.cancelTransfer(id); loadTransfers() }
    catch (e) { setError((e as Error).message) }
  }

  const filtered = transfers.filter(t => {
    if (filter === 'all') return true
    return t.status === filter
  })

  const stats = {
    total: transfers.length,
    active: transfers.filter(t => t.status === 'transferring' || t.status === 'pending').length,
    completed: transfers.filter(t => t.status === 'completed').length,
    failed: transfers.filter(t => t.status === 'failed').length,
  }

  return (
    <div>
      <div className="page-header">
        <h1>File Transfers</h1>
        <p>Global view of all file transfers across every agent</p>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <div className="stat-grid">
        <div className="stat-card">
          <div className="stat-label">Total Transfers</div>
          <div className="stat-value">{stats.total}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Active</div>
          <div className="stat-value yellow">{stats.active}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Completed</div>
          <div className="stat-value green">{stats.completed}</div>
        </div>
        <div className="stat-card">
          <div className="stat-label">Failed</div>
          <div className="stat-value red">{stats.failed}</div>
        </div>
      </div>

      <div className="card">
        <div className="flex gap-8 mb-16" style={{ alignItems: 'center' }}>
          <div className="card-title" style={{ margin: 0 }}>All Transfers</div>
          <select value={filter} onChange={e => setFilter(e.target.value)} className="form-group" style={{ width: 'auto', marginBottom: 0 }}>
            <option value="all">All</option>
            <option value="transferring">In Progress</option>
            <option value="completed">Completed</option>
            <option value="failed">Failed</option>
            <option value="paused">Paused</option>
            <option value="pending">Pending</option>
            <option value="cancelled">Cancelled</option>
          </select>
          <button className="btn btn-sm" onClick={loadTransfers}><RefreshCw size={14} /> Refresh</button>
        </div>

        {loading ? <div className="loading">Loading transfers…</div> :
         filtered.length === 0 ? (
           <div className="empty-state">
             <FileText size={32} style={{ opacity: 0.3, marginBottom: 12 }} />
             <div>No file transfers yet</div>
             <div style={{ fontSize: 12, marginTop: 8 }}>Transfer files from the Files tab on any agent</div>
           </div>
         ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr><th>Direction</th><th>Agent</th><th>Remote Path</th><th>Progress</th><th>Status</th><th>Size</th><th>SHA256</th><th>Started</th><th>Actions</th></tr>
              </thead>
              <tbody>
                {filtered.map(t => {
                  const pct = t.total_size > 0 ? (t.offset / t.total_size * 100) : 0
                  return (
                    <tr key={t.id}>
                      <td>
                        <span className="flex gap-8" style={{ alignItems: 'center' }}>
                          {t.direction === 'upload' ? <Upload size={14} className="green" /> : <Download size={14} className="green" />}
                          {t.direction}
                        </span>
                      </td>
                      <td className="mono dim" style={{ fontSize: 11 }}>{t.agent_id.slice(0, 12)}…</td>
                      <td className="mono" style={{ fontSize: 12, maxWidth: '200px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t.remote_path}</td>
                      <td>
                        <div style={{ width: '100px' }}>
                          <div style={{ fontSize: 11, color: 'var(--text-muted)', marginBottom: 3 }}>{pct.toFixed(1)}%</div>
                          <div style={{ height: 4, background: 'var(--border)', borderRadius: 2, overflow: 'hidden' }}>
                            <div style={{ height: '100%', width: `${pct}%`, background: 'var(--green)', boxShadow: 'var(--green-glow-dim)', transition: 'width 0.3s' }} />
                          </div>
                        </div>
                      </td>
                      <td><TransferStatusBadge status={t.status} /></td>
                      <td className="dim mono" style={{ fontSize: 11 }}>{formatSize(t.total_size)}</td>
                      <td className="dim mono" style={{ fontSize: 10 }}>{t.sha256 ? t.sha256.slice(0, 16) + '…' : '—'}</td>
                      <td className="dim" style={{ fontSize: 11 }}>{new Date(t.created_at).toLocaleString()}</td>
                      <td>
                        <div className="flex gap-8">
                          {(t.status === 'transferring' || t.status === 'pending') && (
                            <>
                              <button className="btn btn-sm" onClick={() => handlePause(t.id)} title="Pause"><Pause size={14} /></button>
                              <button className="btn btn-danger btn-sm" onClick={() => handleCancel(t.id)} title="Cancel"><Ban size={14} /></button>
                            </>
                          )}
                          {t.status === 'paused' && (
                            <>
                              <button className="btn btn-sm" onClick={() => handleResume(t.id)} title="Resume"><Play size={14} /></button>
                              <button className="btn btn-danger btn-sm" onClick={() => handleCancel(t.id)} title="Cancel"><Ban size={14} /></button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

function TransferStatusBadge({ status }: { status: string }) {
  switch (status) {
    case 'completed': return <span className="badge badge-green"><CheckCircle size={12} /> completed</span>
    case 'transferring': return <span className="badge badge-yellow"><Loader size={12} /> transferring</span>
    case 'failed': return <span className="badge badge-red"><XCircle size={12} /> failed</span>
    case 'paused': return <span className="badge badge-gray"><Pause size={12} /> paused</span>
    case 'cancelled': return <span className="badge badge-red"><Ban size={12} /> cancelled</span>
    case 'pending': return <span className="badge badge-yellow">pending</span>
    default: return <span className="badge badge-gray">{status}</span>
  }
}

function formatSize(b: number): string {
  if (b === 0) return '—'
  if (b < 1024) return `${b} B`
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1073741824) return `${(b / 1048576).toFixed(1)} MB`
  return `${(b / 1073741824).toFixed(1)} GB`
}