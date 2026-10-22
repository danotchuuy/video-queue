import { guildIconUrl, type Server } from '../api'

function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean)
  if (words.length === 0) return '?'
  if (words.length === 1) return Array.from(words[0]).slice(0, 2).join('').toUpperCase()
  return (Array.from(words[0])[0] + Array.from(words[1])[0]).toUpperCase()
}

interface Props {
  server: Pick<Server, 'guild_id' | 'icon' | 'name'>
  size?: number
}

/** Guild icon, or the server name's initials when it has none. */
export default function ServerIcon({ server, size = 28 }: Props) {
  const url = guildIconUrl(server)
  if (url) {
    return <img className="avatar" src={url} alt="" width={size} height={size} />
  }
  return (
    <span className="server-initials" style={{ width: size, height: size, fontSize: size * 0.4 }} aria-hidden="true">
      {initials(server.name)}
    </span>
  )
}
