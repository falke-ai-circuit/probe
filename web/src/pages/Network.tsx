import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { ServerEntry, ChainView, HealthInfo } from '../api/types'
import { TopologyGraph } from '../components/TopologyGraph'
import type { TopologyData } from '../components/TopologyGraph'
import { ServersTable, ChainsTable } from '../components/ServersTable'
import { AddUplinkForm } from '../components/AddUplinkForm'
import { e2ePostureChip, rollupChip } from '../lib/servers-view'
import { Network as NetworkIcon } from 'lucide-react'

// /network (P18-01) — the frontend for the P17 peer backend.
//
// P27-03 (spec P27-01) replaced the P17 peers table with the SERVERS view built
// on `GET /api/v1/servers`. The reason is not cosmetic: the old table rendered
// `link_state`, which P27-02 migrated to the six-value enum and which says
// nothing about carrying. A server can be `connected` and delivering no agent —
// that is the live Radxa→OVH state (P25-22), and the old table showed it as a
// green "connected" chip. There is now ONE table per entity and one vocabulary:
//
//   SERVERS  GET /api/v1/servers — direction, six-value state, uplink type,
//            per-link encryption, carrying truth (§1, §2.3, §5.3)
//   CHAINS   GET /api/v1/chain  — per-path hops with registration_state, so a
//            refused registration is named instead of hidden (§5.3 rule 4)
//   RELAYS   the topology graph's inbound mirrors (agents are on /agents)
//
// Honesty rules carried in from P17-03 and tightened by P27-01 C3: an unmapped
// state renders as an unknown chip, an absent transport as ENCRYPTION UNKNOWN,
// and no badge is green unless the wire value says so (see lib/servers-view.ts).
// The token never crosses the wire (§6): token_present + token_fp only.

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

export default function Network() {
  const [servers, setServers] = useState<ServerEntry[]>([])
  const [chains, setChains] = useState<ChainView[]>([])
  const [health, setHealth] = useState<HealthInfo | null>(null)
  const [topo, setTopo] = useState<TopologyData | null>(null)
  const [error, setError] = useState('')
  const [actionErr, setActionErr] = useState('')
  const [success, setSuccess] = useState('')

  const load = async () => {
    // Each read is independent: a failing /chain must not blank the servers
    // table (a partial view that renders green is worse than a named gap, but a
    // blank table would hide rows the server did answer for).
    const [s, c, h, t] = await Promise.allSettled([
      api.listServers(),
      api.listChains(),
      api.getHealth(),
      api.getTopology() as Promise<TopoRaw>,
    ])
    if (s.status === 'fulfilled') setServers(s.value?.servers || [])
    if (c.status === 'fulfilled') setChains(c.value?.chains || [])
    if (h.status === 'fulfilled') setHealth(h.value)
    if (t.status === 'fulfilled') setTopo(transformTopology(t.value))
    setError(s.status === 'rejected' ? (s.reason as Error).message : '')
  }

  useEffect(() => {
    let alive = true
    const tick = async () => { if (alive) await load() }
    tick()
    const interval = setInterval(tick, 5000)
    return () => { alive = false; clearInterval(interval) }
  }, [])

  const clearFlash = () => { setSuccess(''); setActionErr('') }

  // PEER_* codes must reach the operator, not just the prose: the typed
  // envelope is rendered as CODE: message (P18-01 acceptance #5).
  const fail = (e: unknown) => {
    setActionErr(e instanceof ApiError ? `${e.code}: ${e.message}` : (e as Error).message)
  }

  // addUplink posts the payload the form built via buildUplinkPayload — the same
  // body shape the API accepts (§3.2), through the existing peers path.
  const addUplink = async (payload: Record<string, unknown>) => {
    clearFlash()
    const created = await api.createPeer(payload as Parameters<typeof api.createPeer>[0])
    // §6 honesty in the flash: show the fp the server assigned, never the token.
    setSuccess(`Uplink "${created.id}" created — ${created.uplink_type || 'always-on'}, token sealed (${(created.token_fp || '').slice(0, 12)}…)`)
    await load()
  }

  const toggle = async (row: ServerEntry) => {
    clearFlash()
    try {
      const res = await api.peerAction(row.id, row.enabled ? 'disable' : 'enable')
      setSuccess(`Server "${row.id}": ${res.state}`)
      await load()
    } catch (e) { fail(e) }
  }

  const remove = async (row: ServerEntry) => {
    clearFlash()
    try {
      await api.deletePeer(row.id)
      setSuccess(`Server "${row.id}" removed`)
      await load()
    } catch (e) { fail(e) }
  }

  const dial = async (row: ServerEntry) => {
    clearFlash()
    try {
      // §3.3: 'dial' is the manual-only action; the server refuses it on an
      // always-on peer (409 PEER_NOT_MANUAL) and outside a scheduled window.
      const res = await api.peerAction(row.id, 'dial')
      setSuccess(`Server "${row.id}": ${res.state}`)
      await load()
    } catch (e) { fail(e) }
  }

  const serverEntry = topo?.server
  const viaRelayCount = (topo?.agents || []).filter(a => a.relayed).length
  const posture = e2ePostureChip(health?.e2e)
  const rollup = rollupChip(health?.uplinks_total, health?.uplinks_connected, health?.uplinks_carrying)
  const uplinks = servers.filter(s => s.direction === 'uplink' || s.direction === 'inbound')

  return (
    <div>
      <div className="page-header">
        <h1><NetworkIcon size={20} style={{ verticalAlign: '-3px', marginRight: 8 }} />Network</h1>
        <p>Servers (uplinks this server dials or accepts), their chains, and the relays carrying agents</p>
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
          {/* Both chips bind to /health fields, never to a constant: the roll-up
              is the §5.3 rule-6 glance ("never ok while an uplink carries
              nothing") and the posture chip surfaces cipher_error. */}
          <span className="meta-item">
            <span className="dim">uplinks</span>
            <span className={rollup.className} data-testid="health-uplink-rollup">{rollup.label}</span>
          </span>
          <span className="meta-item">
            <span className="dim">posture</span>
            <span className={posture.className} data-testid="health-e2e-posture">{posture.label}</span>
          </span>
        </div>
      </div>

      <TopologyGraph data={topo} />

      <div className="card">
        <div className="card-title">SERVERS — uplinks ({uplinks.length})</div>
        <ServersTable
          rows={uplinks}
          onToggle={toggle}
          onDelete={remove}
          onDial={dial}
        />
        <div className="dim" style={{ fontSize: 12, marginTop: 6 }}>
          A server row is a link, not an agent. <span className="mono">auth-rejected</span> and{' '}
          <span className="mono">version-mismatch</span> are terminal until an operator acts;{' '}
          <span className="mono">retrying</span> keeps dialing with backoff;{' '}
          <span className="mono">manual</span> only dials when you say so. The TLS badge describes the
          LINK's transport — agent-level E2E badges are on /agents and are never inherited here.
        </div>
      </div>

      <div className="card">
        <div className="card-title">CHAINS — per-path registration truth ({chains.length})</div>
        <ChainsTable chains={chains} />
        <div className="dim" style={{ fontSize: 12, marginTop: 6 }}>
          A server hop whose registration was <span className="mono">refused</span> folds the chain to
          degraded with the failing hop named: an agent whose registration was refused is never a
          healthy chain terminating at “server”.
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
        <div className="card-title">ADD UPLINK</div>
        <AddUplinkForm onSubmit={addUplink} />
      </div>
    </div>
  )
}
