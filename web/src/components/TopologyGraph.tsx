import { useState, useEffect, useCallback, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import { Maximize2, Minimize2, RefreshCw, X } from 'lucide-react'

// --- Types ---

interface TopologyNode {
  id: string
  name: string
  version?: string
  type: 'server' | 'agent' | 'relay'
  connected: boolean
  parent?: string  // parent node id (for edge drawing)
  relayed?: boolean // true if connection goes through a relay
}

interface TopologyData {
  server: { id: string; name: string; version?: string }
  agents: TopologyNode[]
  relays: TopologyNode[]
}

// --- Layout helpers ---

interface PositionedNode extends TopologyNode {
  x: number
  y: number
}

function computeLayout(data: TopologyData, width: number): { nodes: PositionedNode[]; edges: { from: string; to: string; relayed: boolean }[] } {
  const nodes: PositionedNode[] = []
  const edges: { from: string; to: string; relayed: boolean }[] = []
  const cx = width / 2
  const rowGap = 110

  // Server at top center
  nodes.push({ ...data.server, type: 'server', connected: true, x: cx, y: 50 })

  const directAgents = data.agents.filter(a => !a.relayed)
  const relayedAgents = data.agents.filter(a => a.relayed)
  const relays = data.relays || []

  // Direct agents in row 2
  const directSpacing = Math.min(140, width / Math.max(directAgents.length, 1))
  const directStart = cx - (directAgents.length - 1) * directSpacing / 2
  directAgents.forEach((a, i) => {
    const x = directAgents.length === 1 ? cx : directStart + i * directSpacing
    nodes.push({ ...a, type: 'agent', x, y: 50 + rowGap })
    edges.push({ from: data.server.id, to: a.id, relayed: false })
  })

  // Relays in row 2 (appended after direct agents)
  const relayStart = directAgents.length
  const relaySpacing = Math.min(180, width / Math.max(relays.length, 1))
  relays.forEach((r, i) => {
    const idx = relayStart + i
    const x = relays.length === 1 && directAgents.length === 0 ? cx : cx - (relays.length - 1) * relaySpacing / 2 + i * relaySpacing
    nodes.push({ ...r, type: 'relay', x, y: 50 + rowGap })
    edges.push({ from: data.server.id, to: r.id, relayed: false })
  })

  // Relayed agents branch down from their parent relay
  const agentsPerRelay: Record<string, TopologyNode[]> = {}
  relayedAgents.forEach(a => {
    const parent = a.parent || ''
    if (!agentsPerRelay[parent]) agentsPerRelay[parent] = []
    agentsPerRelay[parent].push(a)
  })

  Object.entries(agentsPerRelay).forEach(([relayId, agentList]) => {
    const relay = nodes.find(n => n.id === relayId)
    if (!relay) return
    const spacing = 100
    const start = relay.x - (agentList.length - 1) * spacing / 2
    agentList.forEach((a, i) => {
      const x = agentList.length === 1 ? relay.x : start + i * spacing
      nodes.push({ ...a, type: 'agent', x, y: relay.y + rowGap })
      edges.push({ from: relayId, to: a.id, relayed: true })
    })
  })

  return { nodes, edges }
}

// --- Node shape renderers ---

function NodeShape({ node, onClick, onDragStart }: {
  node: PositionedNode
  onClick: () => void
  onDragStart: (e: React.MouseEvent) => void
}) {
  const color = node.connected ? '#00ff41' : '#555'
  const glow = node.connected ? '0 0 8px rgba(0,255,65,0.6)' : 'none'
  const label = node.name || node.id
  const sub = node.version ? `v${node.version}` : ''

  if (node.type === 'server') {
    // Hexagon
    const s = 28
    const pts = [
      [node.x, node.y - s],
      [node.x + s * 0.866, node.y - s * 0.5],
      [node.x + s * 0.866, node.y + s * 0.5],
      [node.x, node.y + s],
      [node.x - s * 0.866, node.y + s * 0.5],
      [node.x - s * 0.866, node.y - s * 0.5],
    ].map(p => p.join(',')).join(' ')
    return (
      <g style={{ cursor: 'pointer' }} onClick={onClick} onMouseDown={onDragStart}>
        <polygon points={pts} fill="rgba(0,255,65,0.08)" stroke={color} strokeWidth={2}
          style={{ filter: `drop-shadow(${glow})` }} />
        <text x={node.x} y={node.y + 48} textAnchor="middle" fill={color} fontSize={11} fontFamily="monospace">{label}</text>
        {sub && <text x={node.x} y={node.y + 62} textAnchor="middle" fill="#5a7a5a" fontSize={9} fontFamily="monospace">{sub}</text>}
      </g>
    )
  }

  if (node.type === 'relay') {
    // Square
    const s = 24
    return (
      <g style={{ cursor: 'pointer' }} onClick={onClick} onMouseDown={onDragStart}>
        <rect x={node.x - s} y={node.y - s} width={s * 2} height={s * 2} rx={4}
          fill="rgba(0,255,65,0.08)" stroke={color} strokeWidth={2}
          style={{ filter: `drop-shadow(${glow})` }} />
        <text x={node.x} y={node.y + 42} textAnchor="middle" fill={color} fontSize={11} fontFamily="monospace">{label}</text>
        {sub && <text x={node.x} y={node.y + 56} textAnchor="middle" fill="#5a7a5a" fontSize={9} fontFamily="monospace">{sub}</text>}
      </g>
    )
  }

  // Agent = circle
  const r = 22
  return (
    <g style={{ cursor: 'pointer' }} onClick={onClick} onMouseDown={onDragStart}>
      <circle cx={node.x} cy={node.y} r={r} fill="rgba(0,255,65,0.08)" stroke={color} strokeWidth={2}
        style={{ filter: `drop-shadow(${glow})` }} />
      <text x={node.x} y={node.y + 42} textAnchor="middle" fill={color} fontSize={11} fontFamily="monospace">{label}</text>
      {sub && <text x={node.x} y={node.y + 56} textAnchor="middle" fill="#5a7a5a" fontSize={9} fontFamily="monospace">{sub}</text>}
    </g>
  )
}

// --- Main component ---

export function TopologyGraph() {
  const navigate = useNavigate()
  const [data, setData] = useState<TopologyData | null>(null)
  const [error, setError] = useState('')
  const [fullscreen, setFullscreen] = useState(false)
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const containerRef = useRef<HTMLDivElement>(null)
  const dragState = useRef<{ type: 'pan' | 'node'; id?: string; startX: number; startY: number; origX?: number; origY?: number } | null>(null)
  const [nodeOverrides, setNodeOverrides] = useState<Record<string, { x: number; y: number }>>({})
  const [selectedNode, setSelectedNode] = useState<TopologyNode | null>(null)
  const [reconfigureUrl, setReconfigureUrl] = useState('')
  const [reconfigureToken, setReconfigureToken] = useState('')
  const [reconfigureResult, setReconfigureResult] = useState('')
  const [showReconfigureAll, setShowReconfigureAll] = useState(false)

  const fetchTopology = useCallback(async () => {
    try {
      const raw = await api.getTopology() as { nodes: any[]; edges: any[] }
      // Transform API response to component-expected format
      const serverNode = raw.nodes.find(n => n.type === 'server')
      const agentNodes = raw.nodes.filter(n => n.type === 'agent')
      const relayNodes = raw.nodes.filter(n => n.type === 'relay')

      // Build parent map from edges
      const parentMap: Record<string, string> = {}
      for (const e of raw.edges) {
        parentMap[e.from] = e.to
      }

      const transform = (n: any): TopologyNode => ({
        id: n.id,
        name: n.name,
        version: n.version,
        type: n.type,
        connected: true,
        parent: parentMap[n.id],
        relayed: n.relayed || n.id.startsWith('relay/'),
      })

      const transformed: TopologyData = {
        server: serverNode ? { id: serverNode.id, name: serverNode.name, version: serverNode.version } : { id: 'server', name: 'server' },
        agents: agentNodes.map(transform),
        relays: relayNodes.map(transform),
      }
      setData(transformed)
      setError('')
    } catch (e) {
      setError((e as Error).message)
    }
  }, [])

  useEffect(() => {
    fetchTopology()
    const interval = setInterval(fetchTopology, 5000)
    return () => clearInterval(interval)
  }, [fetchTopology])

  const width = fullscreen ? window.innerWidth - 40 : 900
  const height = fullscreen ? window.innerHeight - 80 : 340

  const { nodes, edges } = data ? computeLayout(data, width) : { nodes: [] as PositionedNode[], edges: [] as { from: string; to: string; relayed: boolean }[] }

  // Apply drag overrides
  const positionedNodes = nodes.map(n => {
    const ov = nodeOverrides[n.id]
    return ov ? { ...n, x: ov.x, y: ov.y } : n
  })

  const nodeById = (id: string) => positionedNodes.find(n => n.id === id)

  const handleNodeClick = (id: string) => {
    if (id === data?.server.id) {
      // Server node clicked — open reconfigure-all dialog
      setShowReconfigureAll(true)
      setReconfigureUrl('')
      setReconfigureResult('')
      return
    }
    // Agent/relay node clicked — open edit dialog
    const node = [...(data?.agents || []), ...(data?.relays || [])].find(n => n.id === id)
    if (node) {
      setSelectedNode(node)
      setReconfigureUrl('')
      setReconfigureResult('')
    }
  }

  const handleReconfigureAgent = async () => {
    if (!selectedNode || !reconfigureUrl) return
    try {
      setReconfigureResult('Sending...')
      const res = await api.reconfigureAgent(selectedNode.id, reconfigureUrl, reconfigureToken || undefined)
      setReconfigureResult(`✓ Agent reconnecting to ${reconfigureUrl}`)
      setTimeout(() => { setSelectedNode(null); setReconfigureResult('') }, 3000)
    } catch (e) {
      setReconfigureResult(`✗ ${(e as Error).message}`)
    }
  }

  const handleReconfigureAll = async () => {
    if (!reconfigureUrl) return
    try {
      setReconfigureResult('Broadcasting to all agents...')
      const res = await api.reconfigureAll(reconfigureUrl, reconfigureToken || undefined)
      const r = res as { count?: number; new_url?: string }
      setReconfigureResult(`✓ Reconfigure sent to ${r.count || 0} agents → ${r.new_url || reconfigureUrl}`)
      setTimeout(() => { setShowReconfigureAll(false); setReconfigureResult('') }, 4000)
    } catch (e) {
      setReconfigureResult(`✗ ${(e as Error).message}`)
    }
  }

  const handleMouseDown = (e: React.MouseEvent, nodeId?: string) => {
    e.stopPropagation()
    if (nodeId) {
      const n = nodeById(nodeId)
      dragState.current = { type: 'node', id: nodeId, startX: e.clientX, startY: e.clientY, origX: n?.x, origY: n?.y }
    } else {
      dragState.current = { type: 'pan', startX: e.clientX, startY: e.clientY }
    }
  }

  const handleMouseMove = (e: React.MouseEvent) => {
    if (!dragState.current) return
    const ds = dragState.current
    const dx = (e.clientX - ds.startX) / zoom
    const dy = (e.clientY - ds.startY) / zoom
    if (ds.type === 'pan') {
      setPan(prev => ({ x: prev.x + (e.clientX - ds.startX), y: prev.y + (e.clientY - ds.startY) }))
      dragState.current = { ...ds, startX: e.clientX, startY: e.clientY }
    } else if (ds.type === 'node' && ds.id && ds.origX != null && ds.origY != null) {
      setNodeOverrides(prev => ({ ...prev, [ds.id!]: { x: ds.origX! + dx, y: ds.origY! + dy } }))
    }
  }

  const handleMouseUp = () => { dragState.current = null }

  const handleWheel = (e: React.WheelEvent) => {
    const delta = e.deltaY > 0 ? 0.9 : 1.1
    setZoom(z => Math.max(0.3, Math.min(3, z * delta)))
  }

  const containerStyle: React.CSSProperties = {
    position: fullscreen ? 'fixed' : 'relative',
    top: fullscreen ? 0 : 'auto',
    left: fullscreen ? 0 : 'auto',
    width: fullscreen ? '100vw' : '100%',
    height: fullscreen ? '100vh' : height,
    background: '#0a0f0a',
    border: '1px solid var(--border-glow)',
    borderRadius: 4,
    overflow: 'hidden',
    zIndex: fullscreen ? 9999 : 'auto',
    marginBottom: 16,
  }

  return (
    <div ref={containerRef} style={containerStyle}>
      <div style={{ position: 'absolute', top: 8, right: 8, zIndex: 10, display: 'flex', gap: 4 }}>
        <button className="btn btn-sm" onClick={() => setZoom(z => Math.max(0.3, z * 0.9))}>−</button>
        <span className="mono dim" style={{ fontSize: 11, lineHeight: '28px', padding: '0 4px' }}>{(zoom * 100).toFixed(0)}%</span>
        <button className="btn btn-sm" onClick={() => setZoom(z => Math.min(3, z * 1.1))}>+</button>
        <button className="btn btn-sm" onClick={() => { setZoom(1); setPan({ x: 0, y: 0 }); setNodeOverrides({}) }}>Reset</button>
        <button className="btn btn-sm" onClick={() => setFullscreen(f => !f)}>
          {fullscreen ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
        </button>
      </div>

      {error && <div className="error-msg" style={{ margin: 8 }}>{error}</div>}

      {!data && !error && <div className="loading" style={{ padding: 20 }}>Loading topology…</div>}

      {data && (
        <svg
          width="100%"
          height="100%"
          onMouseDown={e => handleMouseDown(e)}
          onMouseMove={handleMouseMove}
          onMouseUp={handleMouseUp}
          onMouseLeave={handleMouseUp}
          onWheel={handleWheel}
          style={{ cursor: dragState.current?.type === 'pan' ? 'grabbing' : 'default' }}
        >
          <g transform={`translate(${pan.x},${pan.y}) scale(${zoom})`}>
            {/* Edges */}
            {edges.map((edge, i) => {
              const from = nodeById(edge.from)
              const to = nodeById(edge.to)
              if (!from || !to) return null
              return (
                <line
                  key={i}
                  x1={from.x} y1={from.y} x2={to.x} y2={to.y}
                  stroke={to.connected ? '#00ff41' : '#555'}
                  strokeWidth={1.5}
                  strokeDasharray={edge.relayed ? '6,4' : 'none'}
                  opacity={0.6}
                />
              )
            })}
            {/* Nodes */}
            {positionedNodes.map(n => (
              <NodeShape
                key={n.id}
                node={n}
                onClick={() => handleNodeClick(n.id)}
                onDragStart={e => handleMouseDown(e, n.id)}
              />
            ))}
          </g>
        </svg>
      )}

      {/* Agent edit dialog */}
      {selectedNode && (
        <div style={{
          position: 'absolute', top: '50%', left: '50%', transform: 'translate(-50%,-50%)',
          background: '#0d130d', border: '1px solid #00ff41', borderRadius: 6, padding: 20,
          minWidth: 360, zIndex: 20, boxShadow: '0 0 20px rgba(0,255,65,0.15)',
        }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 12 }}>
            <strong style={{ color: '#00ff41', fontFamily: 'monospace' }}>{selectedNode.name}</strong>
            <button className="btn btn-sm" onClick={() => setSelectedNode(null)}><X size={14} /></button>
          </div>
          <div style={{ fontSize: 11, color: '#5a7a5a', marginBottom: 12, fontFamily: 'monospace' }}>
            ID: {selectedNode.id} | {selectedNode.relayed ? 'Relayed' : 'Direct'} | {selectedNode.version || '—'}
          </div>
          <button className="btn btn-sm" style={{ width: '100%', marginBottom: 12 }}
            onClick={() => navigate(`/agents/${selectedNode.id}`)}>
            Open Agent Detail →
          </button>
          <div style={{ borderTop: '1px solid #1a2a1a', paddingTop: 12 }}>
            <div style={{ fontSize: 11, color: '#5a7a5a', marginBottom: 6 }}>RECONFIGURE SERVER</div>
            <input
              className="input" style={{ width: '100%', marginBottom: 6, fontSize: 12 }}
              placeholder="ws://new-server:80/ws"
              value={reconfigureUrl}
              onChange={e => setReconfigureUrl(e.target.value)}
            />
            <input
              className="input" style={{ width: '100%', marginBottom: 6, fontSize: 12 }}
              placeholder="Token (optional, keep existing if empty)"
              value={reconfigureToken}
              onChange={e => setReconfigureToken(e.target.value)}
            />
            <button className="btn btn-sm" style={{ width: '100%' }}
              disabled={!reconfigureUrl}
              onClick={handleReconfigureAgent}>
              <RefreshCw size={12} /> Reconnect Agent
            </button>
            {reconfigureResult && (
              <div style={{ fontSize: 11, marginTop: 6, fontFamily: 'monospace', color: reconfigureResult.startsWith('✓') ? '#00ff41' : '#ff4444' }}>
                {reconfigureResult}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Reconfigure all dialog (click server node) */}
      {showReconfigureAll && (
        <div style={{
          position: 'absolute', top: '50%', left: '50%', transform: 'translate(-50%,-50%)',
          background: '#0d130d', border: '1px solid #00ff41', borderRadius: 6, padding: 20,
          minWidth: 400, zIndex: 20, boxShadow: '0 0 20px rgba(0,255,65,0.15)',
        }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 12 }}>
            <strong style={{ color: '#00ff41', fontFamily: 'monospace' }}>Reconfigure All Agents</strong>
            <button className="btn btn-sm" onClick={() => setShowReconfigureAll(false)}><X size={14} /></button>
          </div>
          <div style={{ fontSize: 11, color: '#5a7a5a', marginBottom: 12 }}>
            Broadcast new server address to all connected agents. They will save the new config and reconnect.
          </div>
          <input
            className="input" style={{ width: '100%', marginBottom: 6, fontSize: 12 }}
            placeholder="ws://new-server:80/ws"
            value={reconfigureUrl}
            onChange={e => setReconfigureUrl(e.target.value)}
          />
          <input
            className="input" style={{ width: '100%', marginBottom: 6, fontSize: 12 }}
            placeholder="Token (optional, keep existing if empty)"
            value={reconfigureToken}
            onChange={e => setReconfigureToken(e.target.value)}
          />
          <button className="btn btn-sm" style={{ width: '100%' }}
            disabled={!reconfigureUrl}
            onClick={handleReconfigureAll}>
            <RefreshCw size={12} /> Broadcast to All Agents
          </button>
          {reconfigureResult && (
            <div style={{ fontSize: 11, marginTop: 6, fontFamily: 'monospace', color: reconfigureResult.startsWith('✓') ? '#00ff41' : '#ff4444' }}>
              {reconfigureResult}
            </div>
          )}
        </div>
      )}
    </div>
  )
}