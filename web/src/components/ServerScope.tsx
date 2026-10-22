import { useEffect } from 'react'
import { Link, Outlet, useParams } from 'react-router'
import { ServerContext, setLastServerId, useSession } from '../session'

/** Resolves :serverId to a server and provides it via useServer(). */
export default function ServerScope() {
  const { servers } = useSession()
  const sid = Number(useParams().serverId)
  const server = servers.find((s) => s.id === sid) ?? null

  useEffect(() => {
    if (server) setLastServerId(server.id)
  }, [server])

  if (!server) {
    return (
      <p>
        Server not found. <Link to="/">Go home</Link>
      </p>
    )
  }
  return (
    <ServerContext.Provider value={server}>
      <Outlet />
    </ServerContext.Provider>
  )
}
