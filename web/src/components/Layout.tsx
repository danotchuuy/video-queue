import { useEffect, useMemo } from 'react'
import { Link, NavLink, Outlet, matchPath, useLocation, useNavigate } from 'react-router'
import { getMe, listServers, logout, setUnauthorizedHandler } from '../api'
import { allows, firstAllowedPath, SECTIONS } from '../sections'
import { SessionContext } from '../session'
import { useFetch } from '../useFetch'
import ServerIcon from './ServerIcon'
import UserMenu from './UserMenu'
import { ErrorBox, Loading } from './Status'

export default function Layout() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const meState = useFetch(getMe)
  const serversState = useFetch(listServers)
  const me = meState.data
  const servers = serversState.data
  const reloadServers = serversState.reload

  useEffect(() => {
    setUnauthorizedHandler(() => navigate('/login', { replace: true }))
    return () => setUnauthorizedHandler(null)
  }, [navigate])

  const session = useMemo(() => (me && servers ? { me, servers, reloadServers } : null), [me, servers, reloadServers])

  async function onLogout() {
    try {
      await logout()
    } finally {
      navigate('/login', { replace: true })
    }
  }

  if (meState.loading || serversState.loading) {
    if (!me || !servers) return <main className="container"><Loading /></main>
  }
  if (!session) {
    // A 401 is already redirecting to /login; anything else is a real error.
    const error = meState.error ?? serversState.error
    const retry = () => {
      meState.reload()
      serversState.reload()
    }
    return <main className="container">{error && <ErrorBox message={error} onRetry={retry} />}</main>
  }

  const user = session.me.user
  const canManage = session.me.can_register || session.servers.some((s) => s.can_manage)

  const m = matchPath({ path: '/s/:serverId/*', end: false }, pathname)
  const adminM = matchPath('/manage/servers/:serverId', pathname)
  const currentId = Number(m?.params.serverId ?? adminM?.params.serverId)
  const current = session.servers.find((s) => s.id === currentId) ?? null
  const navServer = m ? current : null
  // Section being viewed (e.g. "/pipeline"), kept when switching servers.
  const rest = m?.params['*'] ?? ''
  const section =
    SECTIONS.filter((s) => s.path && (rest === s.path.slice(1) || rest.startsWith(`${s.path.slice(1)}/`))).sort(
      (a, b) => b.path.length - a.path.length,
    )[0]?.path ?? ''

  return (
    <>
      <header className="app-header">
        <div className="container header-inner">
          <Link to="/" className="brand">Video Queue</Link>
          {session.servers.length > 0 && (
            <div className="switcher">
              {current && <ServerIcon server={current} />}
              <select
                aria-label="Server"
                value={current ? String(current.id) : ''}
                onChange={(e) => {
                  const target = session.servers.find((x) => x.id === Number(e.target.value))
                  const keep = SECTIONS.find((x) => x.path === section)
                  const path = target && keep && !allows(target, keep.key) ? (firstAllowedPath(target) ?? '') : section
                  navigate(`/s/${e.target.value}${path}`)
                }}
              >
                {!current && (
                  <option value="" disabled>
                    Select server
                  </option>
                )}
                {session.servers.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
            </div>
          )}
          <nav className="nav">
            {navServer &&
              SECTIONS.filter((n) => allows(navServer, n.key)).map((n) => (
                <NavLink key={n.path} to={`/s/${navServer.id}${n.path}`} end={n.end}>
                  {n.label}
                </NavLink>
              ))}
          </nav>
          <UserMenu user={user} canManage={canManage} onLogout={onLogout} />
        </div>
      </header>
      {/* The kanban boards (pipeline, clip board) use the full window width. */}
      <main
        className={
          matchPath('/s/:serverId/pipeline', pathname) || matchPath('/s/:serverId/clips/board', pathname)
            ? 'container wide'
            : 'container'
        }
      >
        <SessionContext.Provider value={session}>
          <Outlet />
        </SessionContext.Provider>
      </main>
    </>
  )
}
