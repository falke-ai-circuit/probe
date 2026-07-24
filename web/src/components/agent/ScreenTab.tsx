import { useState, useRef, useEffect, useCallback } from 'react'
import { api } from '../../api/client'
import { Camera, Video, Square, Monitor, MousePointer, Keyboard } from 'lucide-react'

export function ScreenTab({ agentId }: { agentId: string }) {
  const [screenshot, setScreenshot] = useState('')
  const [error, setError] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [streamMode, setStreamMode] = useState<'screenshot' | 'stream'>('screenshot')
  const [fps, setFps] = useState(5)
  const [quality, setQuality] = useState(60)
  const [interactive, setInteractive] = useState(true)
  const [frameInfo, setFrameInfo] = useState({ seq: 0, w: 0, h: 0, fpsActual: 0 })
  const streamRef = useRef<ReturnType<typeof setInterval>>()
  const frameCountRef = useRef(0)
  const fpsTimerRef = useRef<ReturnType<typeof setInterval>>()
  const imgRef = useRef<HTMLImageElement>(null)
  const streamIdRef = useRef('')

  const parseFrame = (data: unknown): string => {
    if (typeof data === 'string') return data.startsWith('data:') ? data : `data:image/jpeg;base64,${data}`
    const d = data as { frame?: string; data?: string; image?: string; base64?: string; url?: string; screenshot?: string; format?: string; width?: number; height?: number; seq_num?: number }
    const fmt = d.format || 'jpeg'
    if (d.frame) return d.frame.startsWith('data:') ? d.frame : `data:image/${fmt};base64,${d.frame}`
    if (d.data) return `data:image/${fmt};base64,${d.data}`
    if (d.image) return d.image.startsWith('data:') ? d.image : `data:image/${fmt};base64,${d.image}`
    if (d.base64) return `data:image/${fmt};base64,${d.base64}`
    if (d.url) return d.url
    if (d.screenshot) return d.screenshot.startsWith('data:') ? d.screenshot : `data:image/${fmt};base64,${d.screenshot}`
    return ''
  }

  const capture = async () => {
    setError('')
    try {
      const res = await api.capture(agentId)
      const img = parseFrame(res)
      if (img) {
        setScreenshot(img)
        const d = res as { width?: number; height?: number }
        setFrameInfo(prev => ({ ...prev, w: d.width || 0, h: d.height || 0 }))
      }
    } catch (e) { setError((e as Error).message) }
  }

  const startStream = async () => {
    setError('')
    setStreaming(true)
    frameCountRef.current = 0

    // Start agent-side streaming — agent captures frames and pushes via WebSocket
    try {
      const res = await api.streamStart(agentId, 0, fps, quality)
      const d = res as { stream_id?: string }
      if (d.stream_id) streamIdRef.current = d.stream_id
    } catch (e) {
      // Fallback to HTTP polling capture if stream-start fails
      console.warn('stream-start failed, falling back to capture polling:', e)
    }

    const interval = Math.max(100, Math.round(1000 / fps))

    const tick = async () => {
      try {
        // Try stream-frame polling first (lightweight, no PowerShell spawn)
        const frameData = await api.streamFrame(agentId)
        const img = parseFrame(frameData)
        if (img) {
          setScreenshot(img)
          frameCountRef.current++
          const d = frameData as { width?: number; height?: number; seq_num?: number }
          setFrameInfo(prev => ({ ...prev, seq: d.seq_num || prev.seq + 1, w: d.width || prev.w, h: d.height || prev.h }))
        }
      } catch {
        // Fallback to direct capture
        try {
          const res = await api.capture(agentId)
          const img = parseFrame(res)
          if (img) { setScreenshot(img); frameCountRef.current++ }
        } catch (e) { setError((e as Error).message); stopStream() }
      }
    }

    tick()
    streamRef.current = setInterval(tick, interval)

    // FPS counter
    fpsTimerRef.current = setInterval(() => {
      setFrameInfo(prev => ({ ...prev, fpsActual: frameCountRef.current }))
      frameCountRef.current = 0
    }, 1000)
  }

  const stopStream = useCallback(() => {
    setStreaming(false)
    if (streamRef.current) { clearInterval(streamRef.current); streamRef.current = undefined }
    if (fpsTimerRef.current) { clearInterval(fpsTimerRef.current); fpsTimerRef.current = undefined }
    if (streamIdRef.current) {
      api.streamStop(agentId, streamIdRef.current).catch(() => {})
      streamIdRef.current = ''
    }
  }, [agentId])

  useEffect(() => { return () => { stopStream() } }, [stopStream])

  // Mouse click on screen image
  const handleMouseClick = async (e: React.MouseEvent<HTMLImageElement>) => {
    if (!interactive || !imgRef.current) return
    const img = imgRef.current
    const rect = img.getBoundingClientRect()
    // Scale click coordinates to actual screen resolution
    const scaleX = (img.naturalWidth || frameInfo.w || rect.width) / rect.width
    const scaleY = (img.naturalHeight || frameInfo.h || rect.height) / rect.height
    const x = Math.round((e.clientX - rect.left) * scaleX)
    const y = Math.round((e.clientY - rect.top) * scaleY)
    const button = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left'
    try {
      await api.pointerClick(agentId, x, y, button)
    } catch (err) { setError(`Click failed: ${(err as Error).message}`) }
  }

  // Keyboard input when screen area is focused
  const handleKeyDown = async (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (!interactive) return
    // Prevent default browser shortcuts
    if (e.ctrlKey || e.altKey) {
      e.preventDefault()
      const keys: string[] = []
      if (e.ctrlKey) keys.push('Ctrl')
      if (e.altKey) keys.push('Alt')
      if (e.shiftKey) keys.push('Shift')
      const keyName = e.key
      if (keyName !== 'Control' && keyName !== 'Alt' && keyName !== 'Shift') {
        keys.push(keyName)
        try { await api.keyCombo(agentId, keys) } catch (err) { setError(`Key combo failed: ${(err as Error).message}`) }
      }
      return
    }
    // Special keys
    const specialKeys: Record<string, string> = {
      'Enter': 'Enter', 'Tab': 'Tab', 'Escape': 'Escape',
      'Backspace': 'Backspace', 'Delete': 'Delete',
      'ArrowUp': 'Up', 'ArrowDown': 'Down', 'ArrowLeft': 'Left', 'ArrowRight': 'Right',
      'Home': 'Home', 'End': 'End', 'PageUp': 'PageUp', 'PageDown': 'PageDown',
      'F1': 'F1', 'F2': 'F2', 'F3': 'F3', 'F4': 'F4', 'F5': 'F5', 'F6': 'F6',
      'F7': 'F7', 'F8': 'F8', 'F9': 'F9', 'F10': 'F10', 'F11': 'F11', 'F12': 'F12',
    }
    if (specialKeys[e.key]) {
      e.preventDefault()
      try { await api.keyPress(agentId, specialKeys[e.key]) } catch (err) { setError(`Key press failed: ${(err as Error).message}`) }
      return
    }
    // Regular character
    if (e.key.length === 1) {
      e.preventDefault()
      try { await api.textInput(agentId, e.key) } catch (err) { setError(`Text input failed: ${(err as Error).message}`) }
    }
  }

  const handleContextMenu = (e: React.MouseEvent) => {
    if (interactive) e.preventDefault()
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      {error && <div className="error-msg">{error}</div>}
      <div className="toolbar" style={{ flexShrink: 0 }}>
        <button className={`btn btn-sm ${streamMode === 'screenshot' ? 'btn-primary' : ''}`} onClick={() => { stopStream(); setStreamMode('screenshot') }}><Camera size={14} /> Screenshot</button>
        <button className={`btn btn-sm ${streamMode === 'stream' ? 'btn-primary' : ''}`} onClick={() => { stopStream(); setStreamMode('stream') }}><Video size={14} /> Stream</button>
        <span className="toolbar-spacer" />
        {streamMode === 'stream' && (
          <>
            <label className="dim" style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4 }}>
              FPS
              <select value={fps} onChange={e => { setFps(Number(e.target.value)); if (streaming) { stopStream(); setTimeout(startStream, 100) } }} style={{ background: 'var(--bg-input)', color: 'var(--green)', border: '1px solid var(--border-glow)', borderRadius: 'var(--radius)', padding: '2px 6px', fontSize: 11 }}>
                <option value={1}>1</option>
                <option value={2}>2</option>
                <option value={5}>5</option>
                <option value={10}>10</option>
                <option value={15}>15</option>
                <option value={20}>20</option>
              </select>
            </label>
            <label className="dim" style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4 }}>
              Q
              <select value={quality} onChange={e => { setQuality(Number(e.target.value)); if (streaming) { stopStream(); setTimeout(startStream, 100) } }} style={{ background: 'var(--bg-input)', color: 'var(--green)', border: '1px solid var(--border-glow)', borderRadius: 'var(--radius)', padding: '2px 6px', fontSize: 11 }}>
                <option value={30}>30</option>
                <option value={50}>50</option>
                <option value={60}>60</option>
                <option value={80}>80</option>
              </select>
            </label>
          </>
        )}
        <button className={`btn btn-sm ${interactive ? 'btn-primary' : ''}`} onClick={() => setInteractive(!interactive)} title="Toggle mouse/keyboard input">
          {interactive ? <MousePointer size={14} /> : <Keyboard size={14} />} {interactive ? 'Interactive' : 'View Only'}
        </button>
        {streamMode === 'screenshot' ? (
          <button className="btn btn-primary btn-sm" onClick={capture} disabled={streaming}><Camera size={14} /> Capture</button>
        ) : (
          <>
            <span className={`status-dot ${streaming ? 'active' : 'inactive'}`} />
            {streaming ? <button className="btn btn-danger btn-sm" onClick={stopStream}><Square size={14} /> Stop</button> : <button className="btn btn-primary btn-sm" onClick={startStream}><Video size={14} /> Start</button>}
            {streaming && <span className="dim" style={{ fontSize: 11 }}>{frameInfo.fpsActual} fps · {frameInfo.w}x{frameInfo.h}</span>}
          </>
        )}
      </div>
      {screenshot ? (
        <div className="screen-display" style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', cursor: interactive ? 'crosshair' : 'default' }}>
          <img
            ref={imgRef}
            src={screenshot}
            alt="Screen capture"
            style={{ maxWidth: '100%', maxHeight: '100%', borderRadius: 3, objectFit: 'contain' }}
            onClick={handleMouseClick}
            onContextMenu={handleContextMenu}
          />
          <div className="dim" style={{ fontSize: 11, marginTop: 6, textAlign: 'center' }}>
            {streaming ? `Streaming · ${frameInfo.fpsActual} fps · frame #${frameInfo.seq}` : `Captured at ${new Date().toLocaleString()}`}
            {interactive && ' · Click to interact · Keyboard enabled when focused'}
          </div>
        </div>
      ) : (
        <div className="screen-stream" style={{ flex: 1, minHeight: 0 }}>
          <div className="empty-state">
            <Monitor size={32} style={{ opacity: 0.3, marginBottom: 8 }} />
            {streamMode === 'screenshot' ? 'No screenshot captured. Click "Capture" to grab a frame.' : 'Stream not started. Click "Start" for live screen capture.'}
          </div>
        </div>
      )}
    </div>
  )
}