import { createContext, useContext } from 'react'
import type { Me, Server } from './api'

export interface Session {
  me: Me
  servers: Server[]
  reloadServers: () => void
}

export const SessionContext = createContext<Session | null>(null)
export const ServerContext = createContext<Server | null>(null)

const LAST_SERVER_KEY = 'vq_last_server'

export function useSession(): Session {
  const s = useContext(SessionContext)
  if (!s) throw new Error('useSession must be used inside <Layout>')
  return s
}

/** The server of the current /s/:serverId route. */
export function useServer(): Server {
  const s = useContext(ServerContext)
  if (!s) throw new Error('useServer must be used inside a server route')
  return s
}

export function getLastServerId(): number | null {
  try {
    const n = Number(localStorage.getItem(LAST_SERVER_KEY))
    return Number.isInteger(n) && n > 0 ? n : null
  } catch {
    return null
  }
}

export function setLastServerId(id: number) {
  try {
    localStorage.setItem(LAST_SERVER_KEY, String(id))
  } catch {
    // storage unavailable; the preference just isn't remembered
  }
}
