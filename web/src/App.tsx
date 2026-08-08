import { Routes, Route, Navigate } from 'react-router-dom'
import Sidebar from './components/Sidebar'
import Dashboard from './pages/Dashboard'
import Agents from './pages/Agents'
import AgentDetail from './pages/AgentDetail'
import Builder from './pages/Builder'
import Profiles from './pages/Profiles'
import Tasks from './pages/Tasks'
import Transfers from './pages/Transfers'
import Credentials from './pages/Credentials'
import Settings from './pages/Settings'
import Flows from './pages/Flows'
import Login from './pages/Login'
import { getToken } from './api/client'

export default function App() {
  const token = getToken()

  // If not authenticated, show the login page.
  if (!token) {
    return <Login />
  }

  return (
    <div className="app-layout">
      <Sidebar />
      <div className="main-content">
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/agents" element={<Agents />} />
          <Route path="/agents/:id" element={<AgentDetail />} />
          <Route path="/builds" element={<Builder />} />
          <Route path="/profiles" element={<Profiles />} />
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/transfers" element={<Transfers />} />
          <Route path="/credentials" element={<Credentials />} />
          <Route path="/flows" element={<Flows />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </div>
    </div>
  )
}