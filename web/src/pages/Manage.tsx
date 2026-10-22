import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { createServer, listGuilds, type Guild } from '../api'
import ServerIcon from '../components/ServerIcon'
import { ErrorBox, Loading } from '../components/Status'
import { useSession } from '../session'
import { useFetch } from '../useFetch'
import { errMsg } from '../util'

export default function Manage() {
  const { me, servers, reloadServers } = useSession()
  const navigate = useNavigate()
  const guilds = useFetch(listGuilds)
  const [names, setNames] = useState<Record<string, string>>({})
  const [busyId, setBusyId] = useState<string | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})

  async function add(g: Guild) {
    setBusyId(g.id)
    setErrors({ ...errors, [g.id]: '' })
    try {
      const name = (names[g.id] ?? '').trim()
      const created = await createServer({ guild_id: g.id, ...(name ? { name } : {}) })
      reloadServers()
      navigate(`/manage/servers/${created.id}`)
    } catch (e) {
      setErrors((prev) => ({ ...prev, [g.id]: errMsg(e) }))
      setBusyId(null)
    }
  }

  const managed = servers.filter((s) => s.can_manage)
  const available = guilds.data?.guilds.filter((g) => g.server_id === null) ?? []

  return (
    <>
      <h1>Manage servers</h1>
      <h2>Your servers</h2>
      {managed.length === 0 && <p className="muted">You don't manage any registered servers yet.</p>}
      {managed.map((s) => (
        <article key={s.id} className="card card-row">
          <ServerIcon server={s} size={36} />
          <div className="grow">
            <div className="card-title">{s.name}</div>
            <div className="muted small">
              {s.guild_name} · Suggestions sync {s.suggestions_channel_id ? 'on' : 'off'}
            </div>
          </div>
          <Link to={`/manage/servers/${s.id}`}>Settings</Link>
        </article>
      ))}

      {me.can_register && (
        <>
          <h2>Add a server</h2>
          <p className="muted small">
            Only servers where you have Manage Server are listed. Just got the permission? Sign out and in again.
          </p>
          {guilds.error && <ErrorBox message={guilds.error} onRetry={guilds.reload} />}
          {guilds.loading && !guilds.data && <Loading />}
          {guilds.data && available.length === 0 && <p className="muted">No servers available to add.</p>}
          {available.map((g) => (
            <article key={g.id} className="card">
              <div className="card-row">
                <ServerIcon server={{ guild_id: g.id, icon: g.icon, name: g.name }} size={36} />
                <div className="grow card-title">{g.name}</div>
              </div>
              {g.bot_present ? (
                <div className="row actions">
                  <input
                    className="grow"
                    aria-label={`Display name for ${g.name}`}
                    placeholder={`Display name (default: ${g.name})`}
                    maxLength={100}
                    value={names[g.id] ?? ''}
                    onChange={(e) => setNames({ ...names, [g.id]: e.target.value })}
                  />
                  <button type="button" className="primary" disabled={busyId !== null} onClick={() => add(g)}>
                    {busyId === g.id ? 'Adding…' : 'Add'}
                  </button>
                </div>
              ) : (
                <p>
                  <a href={g.invite_url} target="_blank" rel="noreferrer">
                    Invite the bot
                  </a>{' '}
                  <span className="muted small">then refresh this page.</span>
                </p>
              )}
              {errors[g.id] && <ErrorBox message={errors[g.id]} />}
            </article>
          ))}
          {me.is_admin && guilds.data?.invite_url && (
            <p>
              <a href={guilds.data.invite_url} target="_blank" rel="noreferrer">
                Invite the bot to another server
              </a>
              <br />
              <span className="muted small">After inviting the bot, refresh this page to see the new server.</span>
            </p>
          )}
        </>
      )}
    </>
  )
}
