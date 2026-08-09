import { useState, useEffect, useRef, useLayoutEffect, ReactNode } from 'react'

interface ActionMenuProps {
  children: (close: () => void) => ReactNode
  label?: string
  align?: 'right' | 'left'
}

/**
 * ActionMenu — overflow menu with viewport-aware positioning.
 *
 * - Uses position:fixed (not position:absolute) so the menu escapes any
 *   overflow:hidden parent (e.g. the body which has overflow:hidden in
 *   the operator console layout).
 * - Computes coordinates from the trigger's getBoundingClientRect().
 * - If there isn't enough room below the trigger, flips the menu above it.
 * - Closes on outside click and Escape.
 */
export function ActionMenu({ children, label = 'Actions', align = 'right' }: ActionMenuProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: number; left: number; flip: boolean } | null>(null)

  const close = () => setOpen(false)

  // Recompute position whenever the menu opens
  useLayoutEffect(() => {
    if (!open) {
      setPos(null)
      return
    }
    const compute = () => {
      const btn = triggerRef.current
      if (!btn) return
      const rect = btn.getBoundingClientRect()
      const MENU_WIDTH = 180
      const MENU_ESTIMATE_HEIGHT = 240
      const MARGIN = 8
      const wantLeft = align === 'right' ? rect.right - MENU_WIDTH : rect.left
      // Clamp to viewport
      const maxLeft = window.innerWidth - MENU_WIDTH - 8
      const left = Math.max(8, Math.min(wantLeft, maxLeft))
      const spaceBelow = window.innerHeight - rect.bottom - MARGIN
      const flip = spaceBelow < MENU_ESTIMATE_HEIGHT && rect.top > MENU_ESTIMATE_HEIGHT
      const top = flip ? rect.top - MENU_ESTIMATE_HEIGHT - 4 : rect.bottom + 4
      setPos({ top, left, flip })
    }
    compute()
    window.addEventListener('resize', compute)
    window.addEventListener('scroll', compute, true)
    return () => {
      window.removeEventListener('resize', compute)
      window.removeEventListener('scroll', compute, true)
    }
  }, [open, align])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      const t = e.target as Node
      if (
        triggerRef.current && !triggerRef.current.contains(t) &&
        menuRef.current && !menuRef.current.contains(t)
      ) {
        setOpen(false)
      }
    }
    const onEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onEsc)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onEsc)
    }
  }, [open])

  return (
    <>
      <button
        ref={triggerRef}
        className="kebab-btn"
        onClick={() => setOpen(o => !o)}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        ⋯
      </button>
      {open && pos && (
        <div
          ref={menuRef}
          className="kebab-menu-fixed"
          role="menu"
          style={{ top: pos.top, left: pos.left }}
        >
          {children(close)}
        </div>
      )}
    </>
  )
}