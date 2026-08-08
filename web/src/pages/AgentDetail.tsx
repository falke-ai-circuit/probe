import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api } from '../api/client'
import type { AgentRecord, AuditEntry } from '../api/types'
import { StatusBadge } from '../components/StatusBadge'
import { TerminalTab } from '../components/agent/TerminalTab'
import { FilesTab } from '../components/agent/FilesTab'
import { ProcessesTab } from '../components/agent/ProcessesTab'
import { TunnelsTab } from '../components/agent/TunnelsTab'
import { MITMTab } from '../components/agent/MITMTab'
import { DebugTab } from '../components/agent/DebugTab'
import { ScreenTab } from '../components/agent/ScreenTab'
import { ModesTab } from '../components/agent/ModesTab'
import { SurveyTab } from '../components/agent/SurveyTab'
import { ChevronRight, Terminal, FolderTree, Cpu, ArrowLeftRight, Network, Bug, Monitor, ScrollText, Layers, Activity } from 'lucide-react'

const tabs = [
  { name: 'Terminal', icon: Terminal },
  { name: 'Files', icon: FolderTree },
  { name: 'Screen', icon: Monitor },
  { name: 'Processes', icon: Cpu },
  { name: 'Tunnels', icon: ArrowLeftRight },
  { name: 'MITM', icon: Network },
  { name: 'Debug', icon: Bug },
  { name: 'Modes', icon: Layers },
  { name: 'Survey', icon: Activity },
  { name: 'Audit', icon: ScrollText },
] as const
type TabName = typeof tabs[number]['name']

export default function AgentDetail() {
  const { id } = useParams<{ id: string }>()
  const [agent, setAgent] = useState<AgentRecord | null>(null)
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [activeTab, setActiveTab] = useState<TabName>('Terminal')
  const [error, setError] = useState('')

  useEffect(() => {
    if (!id) return
    const load = async () => {
      try {
        const [a, au] = await Promise.all([api.getAgent(id), api.getAgentAudit(id).catch(() => [])])
        setAgent(a); setAudit(au || []); setError('')
      } catch (e) { setError((e as Error).message) }
    }
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [id])

  if (error && !agent) return <div className="error-msg">{error}</div>
  if (!agent) return <div className="loading">Loading agent…</div>

  return (
    <div className="agent-detail-layout">
      <div className="agent-header-bar">
        <div className="breadcrumb">
          <Link to="/agents" className="breadcrumb-link">Agents</Link>
          <ChevronRight size={14} className="breadcrumb-sep" />
          <span className="breadcrumb-current">{agent.name || agent.agent_id}</span>
          <ChevronRight size={14} className="breadcrumb-sep" />
          <span className="breadcrumb-tab">{activeTab}</span>
        </div>
        <span className={`status-dot ${agent.status === 'active' ? 'active' : 'inactive'}`} />
        <StatusBadge status={agent.status} />
        <span className="mono dim" style={{ marginLeft: 'auto', fontSize: 11 }}>{agent.agent_id}</span>
      </div>

      <div className="agent-tabs">
        {tabs.map(tab => {
          const Icon = tab.icon
          return (
            <div key={tab.name} className={`agent-tab ${activeTab === tab.name ? 'active' : ''}`} onClick={() => setActiveTab(tab.name)}>
              <Icon size={15} strokeWidth={1.5} /> {tab.name}
            </div>
          )
        })}
      </div>

      <div className="agent-content">
        {activeTab === 'Terminal' && id && <TerminalTab agentId={id} />}
        {activeTab === 'Files' && id && <FilesTab agentId={id} agentOS={agent.os} />}
        {activeTab === 'Processes' && id && <ProcessesTab agentId={id} />}
        {activeTab === 'Tunnels' && id && <TunnelsTab agentId={id} />}
        {activeTab === 'MITM' && id && <MITMTab agentId={id} />}
        {activeTab === 'Debug' && id && <DebugTab agentId={id} />}
        {activeTab === 'Screen' && id && <ScreenTab agentId={id} />}
        {activeTab === 'Modes' && id && <ModesTab agentId={id} />}
        {activeTab === 'Survey' && id && <SurveyTab agentId={id} />}
        {activeTab === 'Audit' && (
          <div className="card">
            {audit.length === 0 ? <div className="empty-state">No audit entries</div> : (
              <div className="table-container"><table>
                <thead><tr><th>Time</th><th>Action</th><th>Operator</th><th>Result</th></tr></thead>
                <tbody>{audit.map((e, i) => (
                  <tr key={i}><td className="dim">{new Date(e.timestamp).toLocaleString()}</td><td className="mono">{e.action}</td><td className="dim">{e.operator_id || '—'}</td><td className="dim">{e.result || e.error || '—'}</td></tr>
                ))}</tbody>
              </table></div>
            )}
          </div>
        )}
      </div>

      <div className="connection-bar">
        <div className="conn-bar-item"><span className="conn-bar-label">OS</span><span className="conn-bar-value">{agent.os}/{agent.arch}</span></div>
        <div className="conn-bar-item"><span className="conn-bar-label">VER</span><span className="conn-bar-value">{agent.version}</span></div>
        <div className="conn-bar-item"><span className="conn-bar-label">MODE</span><span className="conn-bar-value">{agent.mode}</span></div>
        <div className="conn-bar-item"><span className="conn-bar-label">UP</span><span className="conn-bar-value">{formatUptime(agent.uptime_seconds)}</span></div>
        <div className="conn-bar-item"><span className="conn-bar-label">HP</span><span className="conn-bar-value green">{(agent.health_score * 100).toFixed(0)}%</span></div>
        <div className="conn-bar-item"><span className="conn-bar-label">ERR</span><span className={`conn-bar-value ${agent.error_count > 0 ? 'red' : ''}`}>{agent.error_count}</span></div>
        {agent.resource_usage && (
          <>
            <div className="conn-bar-item"><span className="conn-bar-label">CPU</span><span className="conn-bar-value">{agent.resource_usage.cpu_percent.toFixed(1)}%</span></div>
            <div className="conn-bar-item"><span className="conn-bar-label">MEM</span><span className="conn-bar-value">{agent.resource_usage.memory_mb.toFixed(0)}MB</span></div>
          </>
        )}
        <div className="conn-bar-item"><span className="conn-bar-label">CAPS</span><span className="conn-bar-value">{(agent.capabilities || []).join(', ') || '—'}</span></div>
      </div>
    </div>
  )
}

function formatUptime(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}