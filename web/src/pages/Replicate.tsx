import { useState, useEffect } from 'react'
import { api } from '../api/client'
import type { ReplicaRecord } from '../api/types'

const PERMISSIONS = ['full', 'standard', 'read-only', 'sandboxed']

export default function Replicate() {
  const [replicas, setReplicas] = useState<Record<string, ReplicaRecord>>({})
  const [name, setName] = useState('')
  const [server, setServer] = useState('')
  const [token, setToken] = useState('')
  const [permissions, setPermissions] = useState('full')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const load = async () => {
    try {
      setReplicas(await api.listReplicas())
    } catch {
      /* ignore list errors (e.g. not yet authorized) */
    }
  }

  useEffect(() => { load() }, [])

  const create = async () => {
    setError(''); setSuccess('')
    try {
      const rec = await api.replicateAgent({ name, server, token, mode: 'silent', permissions })
      setSuccess(`Replica '${rec.name}' spawned (pid ${rec.pid})`)
      setName(''); setServer(''); setToken('')
      load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const kill = async (n: string) => {
    setError(''); setSuccess('')
    try {
      await api.killReplica(n)
      setSuccess(`Replica '${n}' killed`)
      load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const list = Object.values(replicas)

  return (
    <div>
      <div className="page-header">
        <h1>Replicator</h1>
        <p>Create a copy of this probe (a child agent) with built-in settings — no config file needed</p>
      </div>

      {error && <div className="error-msg">{error}</div>}
      {success && <div className="success-msg">{success}</div>}

      {/* New replica form */}
      <div className="card">
        <div className="card-title">New Replica</div>
        <div className="form-row mb-16">
          <div className="form-group">
            <label>Name</label>
            <input type="text" value={name} onChange={e => setName(e.target.value)} placeholder="vegas-agent" />
          </div>
          <div className="form-group">
            <label>Server (ws:// or wss://)</label>
            <input type="text" value={server} onChange={e => setServer(e.target.value)} placeholder="ws://host:7701/ws" />
          </div>
          <div className="form-group">
            <label>Token</label>
            <input type="password" value={token} onChange={e => setToken(e.target.value)} placeholder="auth token" />
          </div>
          <div className="form-group">
            <label>Permissions</label>
            <select value={permissions} onChange={e => setPermissions(e.target.value)}>
              {PERMISSIONS.map(p => <option key={p} value={p}>{p}</option>)}
            </select>
          </div>
          <div className="form-group">
            <label>&nbsp;</label>
            <button className="btn btn-primary btn-sm" onClick={create}>Create</button>
          </div>
        </div>
      </div>

      {/* Existing replicas */}
      <div className="card">
        <div className="card-title">Replicas ({list.length})</div>
        {list.length === 0 ? (
          <div className="empty-state">No replicas spawned</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr><th>Name</th><th>PID</th><th>Server</th><th>Permissions</th><th>Status</th><th>Actions</th></tr>
              </thead>
              <tbody>
                {list.map(r => (
                  <tr key={r.name}>
                    <td>{r.name}</td>
                    <td className="mono dim">{r.pid}</td>
                    <td className="mono dim">{r.server}</td>
                    <td>{r.permissions}</td>
                    <td>
                      <span className={`badge ${r.status === 'running' ? 'badge-green' : r.status === 'orphaned' ? 'badge-blue' : 'badge-gray'}`}>
                        {r.status}
                      </span>
                    </td>
                    <td>
                      <button className="btn btn-danger btn-sm" onClick={() => kill(r.name)}>Kill</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
