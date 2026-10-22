import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router'
import type { User } from '../api'

interface Props {
  user: User
  canManage: boolean
  onLogout: () => void
}

/** The signed-in user's avatar and name; clicking opens the account menu. */
export default function UserMenu({ user, canManage, onLogout }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const { pathname } = useLocation()
  // The page the menu was opened on; navigating anywhere else closes it.
  const [openOn, setOpenOn] = useState<string | null>(null)
  const open = openOn === pathname
  const setOpen = (o: boolean) => setOpenOn(o ? pathname : null)
  const name = user.global_name || user.username

  // Close on outside clicks and Escape.
  useEffect(() => {
    if (!open) return
    const onPointer = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpenOn(null)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpenOn(null)
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div className="user" ref={ref}>
      <button
        type="button"
        className="user-button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Account menu for ${name}`}
        onClick={() => setOpen(!open)}
      >
        {user.avatar && (
          <img
            className="avatar"
            src={`https://cdn.discordapp.com/avatars/${user.discord_id}/${user.avatar}.png?size=64`}
            alt=""
            width={28}
            height={28}
          />
        )}
        {/* Without an avatar the name is the only thing to click, so it stays visible on small screens. */}
        <span className={user.avatar ? 'user-name' : undefined}>{name}</span>
        <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="user-menu" role="menu">
          {canManage && (
            <Link to="/manage" role="menuitem">
              Manage servers
            </Link>
          )}
          <button type="button" role="menuitem" onClick={onLogout}>
            Logout
          </button>
        </div>
      )}
    </div>
  )
}
