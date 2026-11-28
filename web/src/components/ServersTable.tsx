import type { ServerEntry, ChainView } from '../api/types'
import {
  stateChip, directionChip, uplinkTypeLabel, encryptionBadge, payloadLabel,
  carryingVerdict, chainVerdict, canDial,
} from '../lib/servers-view'
import { Server as ServerIcon, ArrowUpRight, ArrowDownLeft, Trash2, Power, PowerOff, RefreshCw, Link2 } from 'lucide-react'

// P27-03 (spec P27-01 §1, §2.3, §5.3) — the SERVERS table, presentational only.
//
// Why a separate component: a server row must never be mistakable for an agent
// row (scope item 1), and that claim is asserted in test by rendering THIS
// component and reading the DOM. Keeping the fetch and the form out of it means
// the acceptance probe needs no network and no timers.
//
// Every badge calls into lib/servers-view.ts, where the binding to the API
// value lives. Nothing in this file decides that a thing is healthy: it renders
// what the verdict functions return, and their only green path is an explicit
// wire value (transport === "wss", agents_registered > 0, chain state healthy
// with a non-refused registration).

export interface ServersTableProps {
  rows: ServerEntry[]
  onToggle?: (row: ServerEntry) => void
  onDelete?: (row: ServerEntry) => void
  onDial?: (row: ServerEntry) => void
}

function Chip({ chip, testid }: { chip: { label: string; className: string }; testid: string }) {
  return <span className={chip.className} data-testid={testid}>{chip.label}</span>
}

/** DirectionArrow is the "which side dials" mark: a server-only affordance. */
export function DirectionArrow({ direction }: { direction: string }) {
  const chip = directionChip(direction)
  const Icon = direction === 'inbound' ? ArrowDownLeft : direction === 'uplink' ? ArrowUpRight : Link2
  return (
    <span className={chip.className} data-testid="server-direction" data-direction={direction || 'unknown'} title={chip.label}>
      <Icon size={11} aria-hidden="true" /> {chip.label}
    </span>
  )
}

export function ServersTable({ rows, onToggle, onDelete, onDial }: ServersTableProps) {
  if (rows.length === 0) {
    return (
      <div className="empty-state" data-testid="servers-empty">
        <ServerIcon className="empty-icon" size={56} strokeWidth={1} aria-hidden="true" />
        <h3>No servers configured</h3>
        <p>An uplink is a peer this server dials (or that dials it). Add one below.</p>
      </div>
    )
  }
  return (
    <div className="table-container">
      <table data-testid="servers-table" data-entity="server">
        <thead>
          <tr>
            <th aria-label="icon" />
            <th>Server</th>
            <th>Direction</th>
            <th>Address</th>
            <th>State</th>
            <th>Uplink type</th>
            <th>Encrypted</th>
            <th>Carrying</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(row => {
            const st = stateChip(row.state)
            const enc = encryptionBadge(row.encrypted)
            const carry = carryingVerdict(row)
            const editable = (row.source ?? '') === 'peers'
            return (
              <tr
                key={row.id}
                data-testid={`server-row-${row.id}`}
                data-entity="server"
                data-source={row.source}
                data-state={row.state}
                title={row.state_reason ? `state_reason: ${row.state_reason}` : undefined}
              >
                <td>
                  {/* The server glyph is the primary visual distinguisher from an
                      agent row (agents use their own avatar/status column). */}
                  <ServerIcon size={15} data-testid="server-row-icon" aria-label="server" />
                </td>
                <td>
                  <div className="mono">{row.name || row.id}</div>
                  <div className="dim" style={{ fontSize: 11 }}>
                    {row.role}{row.source === 'bootstrap' ? ' · bootstrap (read-only)' : ''}
                  </div>
                </td>
                <td><DirectionArrow direction={row.direction} /></td>
                <td className="mono">{row.address || '—'}</td>
                <td>
                  <Chip chip={st} testid="server-state-chip" />
                  {!st.known && row.state ? (
                    <div className="dim" style={{ fontSize: 11 }}>unmapped wire value — never rendered healthy</div>
                  ) : null}
                  {st.terminal ? <div className="dim" style={{ fontSize: 11 }}>terminal — needs an operator</div> : null}
                </td>
                <td>
                  <div className="mono" data-testid="server-uplink-type">
                    {uplinkTypeLabel(row.uplink_type, row.schedule_window, row.in_window)}
                  </div>
                  {row.uplink_type === 'scheduled' && !row.in_window ? (
                    <div className="dim" style={{ fontSize: 11 }}>outside window — not dialing</div>
                  ) : null}
                </td>
                <td>
                  <Chip chip={enc} testid="server-encrypted-badge" />
                  <div className="dim" style={{ fontSize: 11 }}>{payloadLabel(row.encrypted)}</div>
                </td>
                <td>
                  <Chip chip={carry.chip} testid="server-carrying-badge" />
                  <div className="dim" style={{ fontSize: 11 }}>{carry.detail}</div>
                  {row.carrying?.last_refusal ? (
                    <div className="dim mono" style={{ fontSize: 11 }} data-testid="server-last-refusal">
                      {row.carrying.last_refusal.code}: {row.carrying.last_refusal.reason}
                    </div>
                  ) : null}
                </td>
                <td>
                  {editable ? (
                    <span style={{ display: 'inline-flex', gap: 4 }}>
                      {canDial(row) ? (
                        <button className="btn btn-sm" data-testid="server-dial" title="Dial this manual uplink now"
                          onClick={() => onDial?.(row)}>
                          <RefreshCw size={12} /> dial
                        </button>
                      ) : null}
                      {row.enabled
                        ? <button className="btn btn-sm" data-testid="server-disable" title="Stop dialing this uplink"
                            onClick={() => onToggle?.(row)}><PowerOff size={12} /> disable</button>
                        : <button className="btn btn-sm" data-testid="server-enable" title="Enable this uplink"
                            onClick={() => onToggle?.(row)}><Power size={12} /> enable</button>}
                      <button className="btn btn-sm" data-testid="server-delete" title="Remove this peer record"
                        onClick={() => onDelete?.(row)}><Trash2 size={12} /></button>
                    </span>
                  ) : (
                    <span className="dim" style={{ fontSize: 11 }} data-testid="server-readonly">read-only</span>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

// ChainsTable renders the §5.3 rule-4 hop truth. A refused registration is
// stated as such; the chain state is never trusted to be "healthy" on its own.
export function ChainsTable({ chains }: { chains: ChainView[] }) {
  if (chains.length === 0) {
    return <div className="empty-state" data-testid="chains-empty">No paths observed yet.</div>
  }
  return (
    <div className="table-container">
      <table data-testid="chains-table">
        <thead>
          <tr>
            <th>Path</th>
            <th>Chain</th>
            <th>Server registration</th>
            <th>Hops</th>
            <th>Failing hop</th>
          </tr>
        </thead>
        <tbody>
          {chains.map(c => {
            const v = chainVerdict(c)
            const serverHop = (c.hops ?? []).find(h => h.role === 'server')
            const reg = serverHop?.registration_state
            return (
              <tr key={c.id} data-testid={`chain-row-${c.id}`} data-chain-state={c.state}>
                <td className="mono">{(c.path || []).join(' → ') || c.id}</td>
                <td><Chip chip={v} testid="chain-state-chip" /></td>
                <td className="mono" data-testid="chain-registration">
                  {reg ? reg : '— (no uplink)'}
                </td>
                <td className="dim" style={{ fontSize: 11 }}>
                  {(c.hops || []).map(h => `${h.role}:${h.id}=${h.state}`).join(' · ')}
                </td>
                <td className="mono" data-testid="chain-failing-hop">
                  {c.failing_hop ? `${c.failing_hop.id}${c.failing_hop.error ? ` — ${c.failing_hop.error}` : ''}` : '—'}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
