import { useState, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { Server, Link, Network } from 'lucide-react'

interface ModeState {
  serve?: { running: boolean }
  connect?: { running: boolean }
  relay?: { running: boolean; listen_addr?: string; upstream_url?: string; agent_tokens?: string[] }
}

export function ModesTab({ agentId }: { agentId: string }) {
  const [modes, setModes] = useState<ModeState>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [output, setOutput] = useState('')

  // Relay editable fields
  const [listenAddr, setListenAddr] = useState('0.0.0.0:8081')
  const [upstreamUrl, setUpstreamUrl] = useState('')
  const [agentTokens, setAgentTokens] = useState('')

  const fetchModes = useCallback(async () => {
    try {
      const data = await api.getAgentMode(agentId) as ModeState
      setModes(data || {})
    } catch { /* silent poll */ }
  }, [agentId])

  useEffect(() => {
    fetchModes()
    const interval = setInterval(fetchModes, 5000)
    return () => clearInterval(interval)
  }, [fetchModes])

  const toggle = async (mode: 'serve' | 'connect' | 'relay', running: boolean) => {
    const action = running ? 'stop' : 'start'
    setBusy(mode); setError(''); setOutput('')
    try {
      let config: Record<string, unknown> | undefined
      if (mode === 'relay' && !running) {
        config = {
          listen_addr: listenAddr,
          upstream_url: upstreamUrl,
          agent_tokens: agentTokens.split('\n').map(t => t.trim()).filter(Boolean),
        }
      }
      await api.agentModeControl(agentId, action, mode, config)
      setOutput(`${mode} ${action}ed`)
      await fetchModes()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  const cards = [
    { key: 'serve' as const, label: 'Serve', icon: Server, desc: 'Listen for incoming agent connections' },
    { key: 'connect' as const, label: 'Connect', icon: Link, desc: 'Connect to an upstream server' },
    { key: 'relay' as const, label: 'Relay', icon: Network, desc: 'Relay traffic between agents and upstream' },
  ]

  return (
    <div>
      {error && <div className="error-msg">{error}</div>}
      {output && <div className="success-msg">{output}</div>}
      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        {cards.map(card => {
          const state = modes[card.key]
          const running = state?.running ?? false
          const Icon = card.icon
          const isBusy = busy === card.key
          return (
            <div key={card.key} className="card" style={{ flex: '1 1 280px', minWidth: 280 }}>
              <div className="card-title" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon size={14} style={{ color: 'var(--green)' }} />
                {card.label}
                <span
                  className={`status-dot ${running ? 'active' : 'inactive'}`}
                  style={{ marginLeft: 'auto' }}
                />
                <span
                  className="mono"
                  style={{ fontSize: 11, color: running ? 'var(--green)' : 'var(--text-muted)' }}
                >
                  {running ? 'RUNNING' : 'STOPPED'}
                </span>
              </div>
              <div className="dim" style={{ fontSize: 12, marginBottom: 12 }}>{card.desc}</div>

              {card.key === 'relay' && !running && (
                <div style={{ marginBottom: 12 }}>
                  <div className="form-group" style={{ marginBottom: 8 }}>
                    <label>Listen Address</label>
                    <input
                      type="text"
                      value={listenAddr}
                      onChange={e => setListenAddr(e.target.value)}
                      placeholder="0.0.0.0:8081"
                      className="mono"
                    />
                  </div>
                  <div className="form-group" style={{ marginBottom: 8 }}>
                    <label>Upstream URL</label>
                    <input
                      type="text"
                      value={upstreamUrl}
                      onChange={e => setUpstreamUrl(e.target.value)}
                      placeholder="https://server:8080"
                      className="mono"
                    />
                  </div>
                  <div className="form-group">
                    <label>Agent Tokens (one per line)</label>
                    <textarea
                      value={agentTokens}
                      onChange={e => setAgentTokens(e.target.value)}
                      placeholder="token-abc-123\ntoken-def-456"
                      rows={3}
                      className="mono"
                      style={{ width: '100%', resize: 'vertical' }}
                    />
                  </div>
                </div>
              )}

              {card.key === 'relay' && running && state && (() => {
                const rs = state as ModeState['relay']
                return (
                  <div className="mono dim" style={{ fontSize: 11, marginBottom: 12 }}>
                    {rs?.listen_addr && <div>Listen: {rs.listen_addr}</div>}
                    {rs?.upstream_url && <div>Upstream: {rs.upstream_url}</div>}
                    {rs?.agent_tokens && rs.agent_tokens.length > 0 && (
                      <div>Tokens: {rs.agent_tokens.length} configured</div>
                    )}
                  </div>
                )
              })()}

              <button
                className={`btn btn-sm ${running ? 'btn-danger' : 'btn-primary'}`}
                onClick={() => toggle(card.key, running)}
                disabled={isBusy}
              >
                {isBusy ? '…' : running ? 'Stop' : 'Start'}
              </button>
            </div>
          )
        })}
      </div>
    </div>
  )
}