import { useState } from 'react'
import { login } from '../api/client'
import HostBadge from '../components/HostBadge'
import { Radar, LogIn } from 'lucide-react'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault(); setError(''); setLoading(true)
    try { await login(username, password); window.location.reload() }
    catch (err) { setError(err instanceof Error ? err.message : 'Login failed') }
    finally { setLoading(false) }
  }

  return (
    <div className="login-container">
      <div className="login-card">
        <div className="login-logo">
          <div className="logo-icon"><Radar size={36} strokeWidth={1.5} /></div>
          <div className="logo-text">PROBE</div>
        </div>
        <div className="login-subtitle">Platform for Remote Operations & Bridge Environment</div>
        {/* P18-06: second badge surface — same identity line, pre-auth, from
            the unauthenticated /health identity block. No token needed. */}
        <HostBadge variant="login" />
        <form onSubmit={handleSubmit}>
          <div className="form-group">
            <label>Username</label>
            <input type="text" value={username} onChange={e => setUsername(e.target.value)} placeholder="admin" autoFocus required />
          </div>
          <div className="form-group">
            <label>Password</label>
            <input type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="••••••••" required />
          </div>
          {error && <div className="error-msg">{error}</div>}
          <button type="submit" className="btn btn-primary login-btn" disabled={loading}>
            <LogIn size={16} /> {loading ? 'Authenticating…' : 'Sign In'}
          </button>
        </form>
      </div>
    </div>
  )
}