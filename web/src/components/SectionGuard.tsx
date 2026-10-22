import type { ReactNode } from 'react'
import { Link, Navigate } from 'react-router'
import { allows, firstAllowedPath, type SectionKey } from '../sections'
import { useServer } from '../session'

/**
 * Route guard for a server section. Without access it shows a message linking to the first allowed section;
 * with `redirect` (the default server route) it goes there instead.
 */
export default function SectionGuard({
  section,
  redirect,
  children,
}: {
  section: SectionKey
  redirect?: boolean
  children: ReactNode
}) {
  const server = useServer()
  if (allows(server, section)) return <>{children}</>
  const first = firstAllowedPath(server)
  // first is never '' here (that would mean videos is allowed), so there is no redirect loop.
  if (redirect && first) return <Navigate to={`/s/${server.id}${first}`} replace />
  return (
    <>
      <h1>No access</h1>
      <p>You don't have access to this section.</p>
      {first !== null && (
        <p>
          <Link to={`/s/${server.id}${first}`}>Go to the first section you can use</Link>
        </p>
      )}
    </>
  )
}
