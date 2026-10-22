import { Navigate, Outlet } from 'react-router'
import { useSession } from '../session'

/** Route guard: users who can neither register nor manage a server are sent home. */
export default function ManageOnly() {
  const { me, servers } = useSession()
  if (!me.can_register && !servers.some((s) => s.can_manage)) return <Navigate to="/" replace />
  return <Outlet />
}
