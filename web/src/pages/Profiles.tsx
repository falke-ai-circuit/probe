import { useState, useEffect } from 'react'
import { api } from '../api/client'
import type { Profile } from '../api/types'

const ALL_CAPS = ['exec', 'filesystem', 'process', 'tunnel', 'mitm', 'debug', 'capture', 'input', 'clipboard']
const OS_OPTIONS = ['windows', 'linux', 'darwin', 'android']
const ARCH_OPTIONS = ['amd64', '386', 'arm64']
const PERMISSIONS = ['read-only', 'standard', 'sandboxed', 'full']

export default function Profiles() {
  const [profiles, setProfiles] = useState<Profile[]>([])
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState({
    name: '',
    os: 'windows',
    arch: 'amd64',
    capabilities: ['exec', 'filesystem'],
    server_url: 'wss://localhost:7700/ws',
    permissions: 'full',
    autostart: true,
  })

  const load = async () => {
    try {
      const p = await api.listProfiles()
      setProfiles(p || [])
    } catch (e) {
      setError((e as Error).message)
    }
  }

  useEffect(() => { load() }, [])

  const toggleCap = (cap: string) => {
    setForm(f => ({
      ...f,
      capabilities: f.capabilities.includes(cap) ? f.capabilities.filter(c => c !== cap) : [...f.capabilities, cap],
    }))
  }

  const createProfile = async () => {
    setError('')
    try {
      await api.createProfile(form as Profile)
      setShowForm(false)
      setForm({ ...form, name: '' })
      load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const deleteProfile = async (id: string) => {
    try {
      await api.deleteProfile(id)
      load()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <div>
      <div className="page-header">
        <h1>Build Profiles</h1>
        <p>Reusable agent build configuration templates</p>
      </div>

      {error && <div className="error-msg">{error}</div>}

      <div className="flex gap-8 mb-16">
        <button className="btn btn-primary btn-sm" onClick={() => setShowForm(!showForm)}>
          {showForm ? 'Cancel' : '+ New Profile'}
        </button>
        <button className="btn btn-sm" onClick={load}>Refresh</button>
      </div>

      {showForm && (
        <div className="card">
          <div className="card-title">New Profile</div>
          <div className="form-group">
            <label>Profile Name</label>
            <input type="text" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="windows-full-agent" />
          </div>
          <div className="form-row">
            <div className="form-group">
              <label>OS</label>
              <select value={form.os} onChange={e => setForm({ ...form, os: e.target.value })}>
                {OS_OPTIONS.map(o => <option key={o} value={o}>{o}</option>)}
              </select>
            </div>
            <div className="form-group">
              <label>Architecture</label>
              <select value={form.arch} onChange={e => setForm({ ...form, arch: e.target.value })}>
                {ARCH_OPTIONS.map(a => <option key={a} value={a}>{a}</option>)}
              </select>
            </div>
          </div>
          <div className="form-group">
            <label>Server URL</label>
            <input type="text" value={form.server_url} onChange={e => setForm({ ...form, server_url: e.target.value })} />
          </div>
          <div className="form-group">
            <label>Permissions</label>
            <select value={form.permissions} onChange={e => setForm({ ...form, permissions: e.target.value })}>
              {PERMISSIONS.map(p => <option key={p} value={p}>{p}</option>)}
            </select>
          </div>
          <div className="card-title">Capabilities</div>
          <div className="checkbox-group">
            {ALL_CAPS.map(cap => (
              <label key={cap} className={`checkbox-item ${form.capabilities.includes(cap) ? 'checked' : ''}`}>
                <input type="checkbox" checked={form.capabilities.includes(cap)} onChange={() => toggleCap(cap)} />
                {cap}
              </label>
            ))}
          </div>
          <div className="form-group mt-16">
            <label>
              <label className="checkbox-item" style={{ display: 'inline-flex' }}>
                <input type="checkbox" checked={form.autostart} onChange={e => setForm({ ...form, autostart: e.target.checked })} />
                Autostart
              </label>
            </label>
          </div>
          <button className="btn btn-primary" onClick={createProfile}>Create Profile</button>
        </div>
      )}

      <div className="card">
        {profiles.length === 0 ? (
          <div className="empty-state">
            <p>No profiles saved</p>
            <button className="btn btn-primary btn-sm" style={{ marginTop: 12 }} onClick={() => setShowForm(true)}>+ New Profile</button>
          </div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr><th>Name</th><th>OS/Arch</th><th>Permissions</th><th>Capabilities</th><th>Created</th><th>Actions</th></tr>
              </thead>
              <tbody>
                {profiles.map(p => (
                  <tr key={p.id}>
                    <td>{p.name}</td>
                    <td className="mono">{p.os}/{p.arch}</td>
                    <td><span className="badge badge-blue">{p.permissions}</span></td>
                    <td className="mono dim">{(p.capabilities || []).join(', ')}</td>
                    <td className="dim">{p.created_at ? new Date(p.created_at).toLocaleString() : '—'}</td>
                    <td>
                      <button className="btn btn-danger btn-sm" onClick={() => deleteProfile(p.id)}>Delete</button>
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