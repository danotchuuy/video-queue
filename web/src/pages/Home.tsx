import { Link, Navigate } from 'react-router'
import { getLastServerId, useSession } from '../session'

/** `/`: redirect to the last-used (or first) server, or show the empty state. */
export default function Home() {
  const { me, servers } = useSession()
  if (servers.length > 0) {
    const last = getLastServerId()
    const target = servers.find((s) => s.id === last) ?? servers[0]
    return <Navigate to={`/s/${target.id}`} replace />
  }
  return (
    <>
      <h1>No servers yet</h1>
      {me.is_admin ? (
        <p>
          No servers are registered. <Link to="/manage">Add one</Link> to get started.
        </p>
      ) : (
        <>
          <p>You don't have access to any servers yet.</p>
          <p>Ask a server admin to add your mod role.</p>
          {me.can_register && (
            <p>
              <Link to="/manage" className="button primary">
                Add your server
              </Link>
            </p>
          )}
        </>
      )}
    </>
  )
}
