import { NavLink } from 'react-router-dom'
import { clearToken } from '../api/client'
import { LayoutDashboard, Monitor, Wrench, Layers, Clock, Settings, LogOut, Radar, ArrowDownUp, KeyRound, GitBranch, Activity, Menu, X, Copy } from 'lucide-react'
import { useState } from 'react'

const navItems = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard },
  { to: '/agents', label: 'Agents', icon: Monitor },
  { to: '/flows', label: 'Flows', icon: GitBranch },
  { to: '/sensors', label: 'Sensors', icon: Activity },
  { to: '/tasks', label: 'Tasks', icon: Clock },
  { to: '/transfers', label: 'Transfers', icon: ArrowDownUp },
  { to: '/credentials', label: 'Credentials', icon: KeyRound },
  { to: '/builds', label: 'Builder', icon: Wrench },
  { to: '/profiles', label: 'Profiles', icon: Layers },
  { to: '/replicate', label: 'Replicator', icon: Copy },
  { to: '/settings', label: 'Settings', icon: Settings },
]

export default function Sidebar() {
  const [open, setOpen] = useState(false)
  const handleLogout = () => { clearToken(); window.location.reload() }
  const close = () => setOpen(false)

  return (
    <>
      {/* Mobile hamburger toggle */}
      <button className="sidebar-toggle" onClick={() => setOpen(o => !o)} aria-label="Toggle navigation">
        {open ? <X size={22} /> : <Menu size={22} />}
      </button>
      {open && <div className="sidebar-backdrop" onClick={close} />}

      <aside className={`sidebar ${open ? 'open' : ''}`}>
        <div className="sidebar-logo">
          <div className="logo-icon"><Radar size={28} strokeWidth={1.5} /></div>
          <div className="logo-text">PROBE</div>
        </div>
        <nav className="sidebar-nav">
          {navItems.map((item) => {
            const Icon = item.icon
            return (
              <NavLink key={item.to} to={item.to} end={item.to === '/'} onClick={close} className={({ isActive }) => isActive ? 'active' : ''}>
                <span className="nav-icon"><Icon size={18} strokeWidth={1.5} /></span>
                {item.label}
              </NavLink>
            )
          })}
        </nav>
        <div className="sidebar-footer">
          <button className="btn btn-sm logout-btn" onClick={handleLogout}>
            <LogOut size={14} /> Logout
          </button>
        </div>
      </aside>
    </>
  )
}
