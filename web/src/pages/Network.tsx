import { Fragment, useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { PeerConfigView, LinkStateDetail } from '../api/types'
import { TopologyGraph } from '../components/TopologyGraph'
import type { TopologyData } from '../components/TopologyGraph'
import { RefreshCw, Power, PowerOff, Trash2, ChevronDown, ChevronRight, Network as NetworkIcon } from 'lucide-react'

// /network (P18-01) — the frontend for the P17 peer backend.
//
// Peers are this server's *outbound* uplinks to upstream peers; relays and
// agents are inbound connections. The topology API (P15-02/P21/P23) already
// carries the server→relay→agent chain including each agent's via/origin
// relay (P23-09 provenance), so the graph here is the SAME TopologyGraph
// component Dashboard uses, fed with one fetch instead of self-polling.
//
// Honesty rules carried over from P17-03: link_state is rendered as distinct
// badges — terminal states (auth-rejected, version-mismatch) are NEVER
// merged into connected; only `connected` itself renders as connected.
// state_detail (refusal codes, version evidence) is shown on expansion.
// The token never crosses the wire (§6): the UI surfaces token_present +
// token_fp and nothing else.

type TopoRaw = {
  server?: { id: string; name?: string; version?: string; active?: boolean }
  nodes: { id: string; type: string; name: string; version?: string; active?: boolean; relayed?: boolean; via?: string; modes?: string; children?: { id: string; type: string; name: string; version?: string; active?: boolean; via?: string }[] }[]
  edges: { from: string; to: string; type?: string }[]
}

// transformTopology converts the raw /topology payload to the shape
// TopologyGraph expects (mirrors the component's own internal transform,
// with the via/origin relay preserved as the parent edge). Agents served
// through a relay arrive NESTED in the relay node's children (server
// topology shape), so they are flattened into the agent list here.
function transformTopology(raw: TopoRaw | null): TopologyData | null {
  if (!raw || !raw.nodes) return null
  const parentMap: Record<string, string> = {}
  for (const e of raw.edges || []) {
    parentMap[e.to] = e.from
  }
  const transform = (n: TopoRaw['nodes'][number]) => ({
    id: n.id,
    name: n.name,
    version: n.version,
    type: (n.type === 'relay' ? 'relay' : n.type === 'agent' ? 'agent' : 'server') as 'server' | 'agent' | 'relay',
    connected: n.active !== false,
    active: n.active !== false,
    parent: n.via || parentMap[n.id],
    relayed: n.relayed || n.id.startsWith('relay/'),
  })
  const agents: TopoRaw['nodes'] = []
  for (const n of raw.nodes) {
    if (n.type === 'agent') {
      agents.push(n)
    }
    // Relay-served agents ride in children (P23-09: via names the origin relay).
    for (const c of n.children || []) {
      if (c.type === 'agent') agents.push({ ...c, relayed: true, via: c.via || n.id })
    }
  }
  const serverNode = raw.nodes.find(n => n.type === 'server')
  return {
    // /topology carries the server as a node, not a separate key.
    server: serverNode
      ? { id: serverNode.id, name: serverNode.name || serverNode.id, version: (serverNode as { version?: string }).version, connected: true, active: true }
      : { id: 'server', name: 'server', connected: true, active: true },
    agents: agents.map(transform),
    relays: (raw.nodes || []).filter(n => n.type === 'relay').map(transform),
  }
}

// Link-state badge classes: one visual language, four distinct states.
// Terminal states (red) must never share a class with connected (green).
const STATE_CLASS: Record<string, string> = {
  connected: 'badge badge-green',
  retrying: 'badge badge-yellow',
  'auth-rejected': 'badge badge-red',
  'version-mismatch': 'badge badge-red',
}

// Terminal = never auto-retries (§4). These need a human, so they state it.
const TERMINAL_STATES = new Set(['auth-rejected', 'version-mismatch'])

function stateLabel(state: string, enabled: boolean) {
  if (!state) return enabled ? 'starting' : 'disabled'
  return state
}

function LinkStateBadge({ peer }: { peer: PeerConfigView }) {
  const cls = STATE_CLASS[peer.link_state] || (peer.enabled ? 'badge badge-gray' : 'badge badge-gray')
  const label = stateLabel(peer.link_state, peer.enabled)
  return (
    <span className={cls}>
      {label}
      {TERMINAL_STATES.has(peer.link_state) ? ' (terminal)' : ''}
    </span>
  )
}

// StateDetail expansion: what the server actually observed. Everything here
// came from the peers view (state_detail), never from guessing.
function StateDetailLines({ detail }: { detail: LinkStateDetail }) {
  const rows: [string, string][] = []
  if (detail.refusal_code) rows.push(['refusal code', detail.refusal_code])
  if (detail.refusal_reason) rows.push(['refusal reason', detail.refusal_reason])
  if (detail.peer_version) rows.push(['peer version', detail.peer_version])
  if (detail.required_min) rows.push(['required min', detail.required_min])
  if (detail.token_unsealable) rows.push(['token unsealable', 'sealed credential could not be unsealed — rotate the token'])
  if (rows.length === 0) return null
  return (
    <div className="dim" style={{ fontSize: 12, marginTop: 6 }} data-testid="peer-state-detail">
      {rows.map(([k, v]) => (
        <div key={k}><span className="mono">{k}:</span> {v}</div>
      ))}
    </div>
  )
}

export default function Network() {
  const [peers, setPeers] = useState<PeerConfigView[]>([])
  const [topo, setTopo] = useState<TopologyData | null>(null)
  const [error, setError] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [success, setSuccess] = useState('')
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  // Connect-a-server form. The token is a write-only field: it goes into the
  // POST body once and is never echoed back anywhere.
  const [formPeerId, setFormPeerId] = useState('')
  const [formUpstream, setFormUpstream] = useState('')
  const [formToken, setFormToken] = useState('')
  const [formListen, setFormListen] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const [p, rawTopo] = await Promise.all([
          api.listPeers(),
          api.getTopology() as Promise<TopoRaw>,
        ])
        if (!alive) return
        setPeers(p?.peers || [])
        setTopo(transformTopology(rawTopo))
        setError('')
      } catch (e) {
        if (alive) setError((e as Error).message)
      }
    }
    load()
    const interval = setInterval(load, 5000)
    return () => { alive = false; clearInterval(interval) }
  }, [])

  const clearFlash = () => { setSuccess(''); setActionErr('') }

  // PEER_* codes must reach the operator, not just the prose: the typed
  // envelope is rendered as CODE: message (P18-01 acceptance #5).
  const fail = (e: unknown) => {
    setActionErr(e instanceof ApiError ? `${e.code}: ${e.message}` : (e as Error).message)
  }

  const addPeer = async () => {
    clearFlash()
    setSubmitting(true)
    try {
      const created = await api.createPeer({
        id: formPeerId.trim(),
        upstream: formUpstream.trim(),
        token: formToken,
        listen: formListen.trim(),
      })
      // §6 honesty in the flash: show the fp the server assigned, never a hint of the token.
      setSuccess(`Peer "${created.id}" created — connect token sealed (${(created.token_fp || '').slice(0, 12)}…)`)
      setFormPeerId(''); setFormUpstream(''); setFormToken(''); setFormListen('')
      const p = await api.listPeers().catch(() => null)
      if (p) setPeers(p.peers || [])
    } catch (e) {
      fail(e)
    } finally {
      setSubmitting(false)
    }
  }

  const doAction = async (id: string, action: 'enable' | 'disable' | 'dial-now') => {
    clearFlash()
    try {
      const res = await api.peerAction(id, action)
      setSuccess(`Peer "${id}": ${res.state}`)
      const p = await api.listPeers().catch(() => null)
      if (p) setPeers(p.peers || [])
    } catch (e) {
      fail(e)
    }
  }

  const doDelete = async (id: string) => {
    clearFlash()
    try {
      await api.deletePeer(id)
      setSuccess(`Peer "${id}" removed`)
      const p = await api.listPeers().catch(() => null)
      if (p) setPeers(p.peers || [])
    } catch (e) {
      fail(e)
    }
  }

  const serverEntry = topo?.server
  const viaRelayCount = (topo?.agents || []).filter(a => a.relayed).length

  return (
    <div>
      <div className="page-header">
        <h1><NetworkIcon size={20} style={{ verticalAlign: '-3px', marginRight: 8 }} />Network</h1>
        <p>Peers (outbound uplinks), relays and agents connected to this server</p>
      </div>

      {error && <div className="error-msg">{error}</div>}
      {success && <div className="success-msg" data-testid="network-flash">{success}</div>}
      {actionErr && <div className="error-msg" data-testid="network-error">{actionErr}</div>}

      <div className="card">
        <div className="card-title">THIS SERVER</div>
        <div className="page-meta" style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
          <span className="meta-item">
            <span className="dim">name</span>
            <span className="meta-value mono">{serverEntry?.name || '…'}</span>
          </span>
          <span className="meta-item">
            <span className="dim">version</span>
            <span className="meta-value mono">{serverEntry?.version ? (serverEntry.version.startsWith('v') ? serverEntry.version : `v${serverEntry.version}`) : '—'}</span>
          </span>
          <span className="meta-item">
            <span className="dim">relays</span>
            <span className="meta-value mono">{topo?.relays?.length ?? '…'}</span>
          </span>
          <span className="meta-item">
            <span className="dim">agents</span>
            <span className="meta-value mono">{topo?.agents?.length ?? '…'}</span>
          </span>
          <span className="meta-item">
            <span className="dim">via relay</span>
            <span className="meta-value mono">{topo ? viaRelayCount : '…'}</span>
          </span>
        </div>
      </div>

      <TopologyGraph data={topo} />

      <div className="card">
        <div className="card-title">PEERS — outbound uplinks ({peers.length})</div>
        {peers.length === 0 ? (
          <div className="empty-state">No peers configured. Use “Connect a server” below.</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th></th>
                  <th>Peer</th>
                  <th>Upstream</th>
                  <th>Link state</th>
                  <th>Token</th>
                  <th>Listen</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {peers.map(peer => (
                  <Fragment key={peer.id}>
                    <tr className="clickable" onClick={() => setExpanded(x => ({ ...x, [peer.id]: !x[peer.id] }))}>
                      <td>{expanded[peer.id] ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</td>
                      <td className="mono">{peer.id}{!peer.enabled ? ' (disabled)' : ''}</td>
                      <td className="mono">{peer.upstream}</td>
                      <td><LinkStateBadge peer={peer} /></td>
                      <td className="dim mono" title={`token fingerprint ${peer.token_fp}`}>
                        {peer.token_present ? `present (${peer.token_fp.slice(0, 12)}…)` : 'missing'}
                      </td>
                      <td className="mono">{peer.listen || '—'}</td>
                      <td onClick={e => e.stopPropagation()}>
                        <span style={{ display: 'inline-flex', gap: 4 }}>
                          {peer.link_state === 'retrying' && (
                            <button className="btn btn-sm" title="Redial now (resets backoff)" onClick={() => doAction(peer.id, 'dial-now')}><RefreshCw size={12} /> dial</button>
                          )}
                          {peer.enabled && !(peer.link_state === 'retrying') && (
                            <button className="btn btn-sm" title="Redial now" onClick={() => doAction(peer.id, 'dial-now')}><RefreshCw size={12} /> dial</button>
                          )}
                          {peer.enabled
                            ? <button className="btn btn-sm" title="Disable this uplink" onClick={() => doAction(peer.id, 'disable')}><PowerOff size={12} /> disable</button>
                            : <button className="btn btn-sm" title="Enable this uplink" onClick={() => doAction(peer.id, 'enable')}><Power size={12} /> enable</button>}
                          <button className="btn btn-sm" title="Remove peer" onClick={() => doDelete(peer.id)}><Trash2 size={12} /></button>
                        </span>
                      </td>
                    </tr>
                    {expanded[peer.id] && (
                      <tr key={`${peer.id}-detail`} style={{ background: 'rgba(0,255,65,0.03)' }}>
                        <td></td>
                        <td colSpan={6}>
                          <StateDetailLines detail={peer.state_detail} />
                          <div className="dim" style={{ fontSize: 12 }}>
                            <div><span className="mono">max_agents:</span> {peer.max_agents}</div>
                            <div><span className="mono">created:</span> {peer.created_at} · <span className="mono">updated:</span> {peer.updated_at}</div>
                          </div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="dim" style={{ fontSize: 12, marginTop: 6 }}>
          auth-rejected and version-mismatch are terminal: the link stays down until you fix the cause (rotate the token, or upgrade the matching side). retrying keeps dialing with backoff.
        </div>
      </div>

      <div className="card">
        <div className="card-title">RELAYS — inbound read-only mirrors (served agents in the graph)</div>
        {(topo?.relays?.length || 0) === 0 ? (
          <div className="empty-state">No relays connected.</div>
        ) : (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Relay</th>
                  <th>Address</th>
                  <th>Agents via this relay</th>
                </tr>
              </thead>
              <tbody>
                {topo!.relays.map(r => {
                  // P23-09: provenance lives on the agent node itself (via), so
                  // the origin read is from the node's own field, not id parsing.
                  const served = (topo?.agents || []).filter(a => a.parent === r.id)
                  return (
                    <tr key={r.id}>
                      <td className="mono">{r.name || r.id}</td>
                      <td className="mono">{r.version || '—'}</td>
                      <td>{served.length === 0 ? <span className="dim">none</span> : served.map(a => a.name).join(', ')}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="card-title">CONNECT A SERVER</div>
        <form onSubmit={e => { e.preventDefault(); addPeer() }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12, alignItems: 'flex-end' }}>
            <label style={{ flex: '1 1 160px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Peer id</div>
              <input className="input" style={{ width: '100%' }} placeholder="edge1" value={formPeerId}
                onChange={e => setFormPeerId(e.target.value)} required pattern="[A-Za-z0-9._-]{1,64}"
                title="letters, digits, . _ - (max 64)" />
            </label>
            <label style={{ flex: '2 1 260px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Upstream</div>
              <input className="input" style={{ width: '100%' }} placeholder="ws://peer-host:7701/ws" value={formUpstream}
                onChange={e => setFormUpstream(e.target.value)} required />
            </label>
            <label style={{ flex: '2 1 220px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Connect token</div>
              <input className="input" style={{ width: '100%' }} type="password" placeholder="token" value={formToken}
                onChange={e => setFormToken(e.target.value)} required autoComplete="new-password" />
            </label>
            <label style={{ flex: '1 1 140px' }}>
              <div className="dim" style={{ fontSize: 12, marginBottom: 4 }}>Local listen port</div>
              <input className="input" style={{ width: '100%' }} placeholder=":0 (auto)" value={formListen}
                onChange={e => setFormListen(e.target.value)} />
            </label>
            <button className="btn" type="submit" disabled={submitting}>
              {submitting ? 'Connecting…' : 'Connect'}
            </button>
          </div>
        </form>
        <div className="dim" style={{ fontSize: 12, marginTop: 6 }}>
          The token is sent once in the connect request, sealed server-side and never displayed again — only its fingerprint shows in the peers table.
        </div>
      </div>
    </div>
  )
}
