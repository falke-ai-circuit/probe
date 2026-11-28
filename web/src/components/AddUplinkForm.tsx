import { useState } from 'react'
import { buildUplinkPayload } from '../lib/servers-view'
import type { UplinkForm } from '../lib/servers-view'
import type { UplinkType } from '../api/types'
import { Plus } from 'lucide-react'

// P27-03 (spec P27-01 §3.2/§3.3) — "Add uplink".
//
// The form collects host + port instead of a raw URL (scope item 3), then
// assembles the upstream through buildUplinkPayload — the SAME function the
// payload test exercises, so what the test proves is what the browser sends.
//
// Two knobs that look similar are deliberately separate (§3.2 NOTE):
//   uplink_type  dial discipline: always-on | scheduled | manual
//   uplink       P25-05R forward target (wire this peer as this server's uplink)
// A standalone manual link is uplink:false + uplink_type:manual. Coupling them
// in the UI would make one of the two unrepresentable.
//
// The token is write-only: it goes into the POST body once and this component
// keeps no copy after submit.

export interface AddUplinkFormProps {
  onSubmit: (payload: Record<string, unknown>) => Promise<void> | void
  disabled?: boolean
}

const BLANK: UplinkForm = {
  id: '',
  host: '',
  port: '7701',
  token: '',
  scheme: 'wss',
  uplinkType: 'manual',
  scheduleStart: '',
  scheduleEnd: '',
  uplink: false,
}

export function AddUplinkForm({ onSubmit, disabled }: AddUplinkFormProps) {
  const [f, setF] = useState<UplinkForm>(BLANK)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const set = <K extends keyof UplinkForm>(k: K, v: UplinkForm[K]) => setF(prev => ({ ...prev, [k]: v }))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    const { payload, error } = buildUplinkPayload(f)
    if (error || !payload) {
      setErr(error || 'invalid form')
      return
    }
    setBusy(true)
    try {
      await onSubmit(payload)
      // Clear the secret first: a retained token in component state is the one
      // thing this form must never keep after a submit.
      setF({ ...BLANK })
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} data-testid="add-uplink-form">
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, alignItems: 'flex-end' }}>
        <label style={{ flex: '1 1 150px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Server id</div>
          <input className="input" style={{ width: '100%' }} data-testid="uplink-id" placeholder="ovh-edge"
            value={f.id} onChange={e => set('id', e.target.value)} required
            pattern="[A-Za-z0-9._-]{1,64}" title="letters, digits, . _ - (max 64)" />
        </label>
        <label style={{ flex: '2 1 190px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Host</div>
          <input className="input" style={{ width: '100%' }} data-testid="uplink-host" placeholder="10.10.10.1"
            value={f.host} onChange={e => set('host', e.target.value)} required />
        </label>
        <label style={{ flex: '0 1 100px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Port</div>
          <input className="input" style={{ width: '100%' }} data-testid="uplink-port" placeholder="7701"
            value={f.port} onChange={e => set('port', e.target.value)} required inputMode="numeric" />
        </label>
        <label style={{ flex: '0 1 110px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Transport</div>
          <select className="input" style={{ width: '100%' }} data-testid="uplink-scheme"
            value={f.scheme} onChange={e => set('scheme', e.target.value as 'wss' | 'ws')}>
            <option value="wss">wss (TLS)</option>
            <option value="ws">ws (cleartext)</option>
          </select>
        </label>
        <label style={{ flex: '2 1 200px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Token</div>
          <input className="input" style={{ width: '100%' }} type="password" data-testid="uplink-token"
            placeholder="peer token (sealed server-side)" value={f.token}
            onChange={e => set('token', e.target.value)} required autoComplete="new-password" />
        </label>
        <label style={{ flex: '0 1 150px' }}>
          <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Uplink type</div>
          <select className="input" style={{ width: '100%' }} data-testid="uplink-type"
            value={f.uplinkType} onChange={e => set('uplinkType', e.target.value as UplinkType)}>
            <option value="always-on">always-on</option>
            <option value="scheduled">scheduled</option>
            <option value="manual">manual</option>
          </select>
        </label>
        {f.uplinkType === 'scheduled' ? (
          <>
            <label style={{ flex: '0 1 110px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Window start</div>
              <input className="input" style={{ width: '100%' }} type="time" data-testid="uplink-window-start"
                value={f.scheduleStart} onChange={e => set('scheduleStart', e.target.value)} required />
            </label>
            <label style={{ flex: '0 1 110px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Window end</div>
              <input className="input" style={{ width: '100%' }} type="time" data-testid="uplink-window-end"
                value={f.scheduleEnd} onChange={e => set('scheduleEnd', e.target.value)} required />
            </label>
          </>
        ) : null}
        <label style={{ flex: '0 1 190px', display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" data-testid="uplink-forward-target" checked={f.uplink}
            onChange={e => set('uplink', e.target.checked)} />
          <span className="dim" style={{ fontSize: 12 }}>use as forward target</span>
        </label>
        <button className="btn btn-primary" type="submit" disabled={disabled || busy} data-testid="uplink-submit">
          <Plus size={14} /> {busy ? 'Adding…' : 'Add uplink'}
        </button>
      </div>
      {err ? <div className="error-msg" data-testid="uplink-form-error" style={{ marginTop: 8 }}>{err}</div> : null}
      <div className="dim" style={{ fontSize: 12, marginTop: 6 }}>
        Posts to the same peers path the API uses, so a UI-added uplink stores the same record shape.
        manual uplinks never dial on their own — use <span className="mono">dial</span> on the row.
        scheduled windows are server-local (start &gt; end wraps midnight).
      </div>
    </form>
  )
}
