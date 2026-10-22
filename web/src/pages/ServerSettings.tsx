import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import {
  deleteServer,
  listChannels,
  listRoles,
  updateServer,
  type Role,
  type Server,
  type ServerPatch,
} from '../api'
import { ErrorBox, Loading } from '../components/Status'
import { useSession } from '../session'
import { useFetch, type FetchState } from '../useFetch'
import { errMsg } from '../util'

function parseIds(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).filter(Boolean))]
}

function sameSet(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((x) => b.includes(x))
}

function roleColor(r: Role): string | undefined {
  return r.color === 0 ? undefined : `#${r.color.toString(16).padStart(6, '0')}`
}

function RoleList({
  legend,
  help,
  roles,
  selected,
  onChange,
}: {
  legend: string
  help: string
  roles: FetchState<Role[]>
  selected: string[]
  onChange: (ids: string[]) => void
}) {
  function toggleRole(id: string, on: boolean) {
    onChange(on ? [...selected, id] : selected.filter((r) => r !== id))
  }

  // Roles saved on the server that Discord no longer lists still show up so they can be removed.
  const knownRoles = roles.data ?? []
  const unknownRoleIds = roles.data ? selected.filter((id) => !knownRoles.some((r) => r.id === id)) : []

  return (
    <fieldset className="roles">
      <legend>{legend}</legend>
      <p className="muted small">{help}</p>
      {roles.error && <ErrorBox message={roles.error} onRetry={roles.reload} />}
      {roles.loading && !roles.data && <Loading />}
      {roles.data && roles.data.length === 0 && <p className="muted">No roles found.</p>}
      {roles.data?.map((r) => (
        <label key={r.id} className="check">
          <input type="checkbox" checked={selected.includes(r.id)} onChange={(e) => toggleRole(r.id, e.target.checked)} />
          <span className="dot" style={{ background: roleColor(r) }} aria-hidden="true" />
          {r.name}
        </label>
      ))}
      {unknownRoleIds.map((id) => (
        <label key={id} className="check">
          <input type="checkbox" checked onChange={() => toggleRole(id, false)} />
          <span className="dot" aria-hidden="true" />
          <span className="muted">Unknown role {id}</span>
        </label>
      ))}
    </fieldset>
  )
}

export default function ServerSettings() {
  const { servers } = useSession()
  const sid = Number(useParams().serverId)
  const server = servers.find((s) => s.id === sid)
  if (!server) {
    return (
      <p>
        Server not found. <Link to="/">Go home</Link>
      </p>
    )
  }
  if (!server.can_manage) {
    return (
      <>
        <p>
          <Link to="/manage">← Manage servers</Link>
        </p>
        <h1>{server.name}</h1>
        <p>You can't manage this server.</p>
      </>
    )
  }
  return <SettingsForm key={`${server.id}/${server.updated_at}`} server={server} />
}

function SettingsForm({ server }: { server: Server }) {
  const { reloadServers } = useSession()
  const navigate = useNavigate()
  const roles = useFetch(() => listRoles(server.id), String(server.id))
  const channels = useFetch(() => listChannels(server.id), String(server.id))

  const [name, setName] = useState(server.name)
  const [roleIds, setRoleIds] = useState<string[]>(server.mod_role_ids)
  const [clipEditorIds, setClipEditorIds] = useState<string[]>(server.clip_editor_role_ids ?? [])
  const [videoEditorIds, setVideoEditorIds] = useState<string[]>(server.video_editor_role_ids ?? [])
  const [allowed, setAllowed] = useState(server.allowed_user_ids.join('\n'))
  const [channel, setChannel] = useState(server.suggestions_channel_id)
  const [clipsChannel, setClipsChannel] = useState(server.clips_channel_id)
  const [blockSelf, setBlockSelf] = useState(server.block_self_suggestions)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const [confirm, setConfirm] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  function buildPatch(): ServerPatch {
    const p: ServerPatch = {}
    if (name.trim() !== server.name) p.name = name.trim()
    if (!sameSet(roleIds, server.mod_role_ids)) p.mod_role_ids = roleIds
    if (!sameSet(clipEditorIds, server.clip_editor_role_ids ?? [])) p.clip_editor_role_ids = clipEditorIds
    if (!sameSet(videoEditorIds, server.video_editor_role_ids ?? [])) p.video_editor_role_ids = videoEditorIds
    const ids = parseIds(allowed)
    if (!sameSet(ids, server.allowed_user_ids)) p.allowed_user_ids = ids
    if (channel !== server.suggestions_channel_id) p.suggestions_channel_id = channel
    if (clipsChannel !== server.clips_channel_id) p.clips_channel_id = clipsChannel
    if (blockSelf !== server.block_self_suggestions) p.block_self_suggestions = blockSelf
    return p
  }

  async function onSave(e: FormEvent) {
    e.preventDefault()
    const p = buildPatch()
    if (Object.keys(p).length === 0) {
      setNotice('Nothing to save.')
      return
    }
    setSaving(true)
    setError(null)
    setNotice(null)
    try {
      await updateServer(server.id, p)
      reloadServers()
      setNotice('Saved.')
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setSaving(false)
    }
  }

  async function onDelete() {
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteServer(server.id)
      reloadServers()
      navigate('/manage')
    } catch (err) {
      setDeleteError(errMsg(err))
      setDeleting(false)
    }
  }

  const knownChannel = channels.data?.some((c) => c.id === channel)
  const knownClipsChannel = channels.data?.some((c) => c.id === clipsChannel)

  return (
    <>
      <p>
        <Link to="/manage">← Manage servers</Link>
      </p>
      <h1>{server.name}</h1>
      <p className="muted small">Discord server: {server.guild_name}</p>

      <form className="form card" onSubmit={onSave}>
        <label>
          Display name
          <input value={name} maxLength={100} required onChange={(e) => setName(e.target.value)} />
        </label>

        <RoleList
          legend="Mod roles"
          help="Members of this Discord server with any checked role can use this server."
          roles={roles}
          selected={roleIds}
          onChange={setRoleIds}
        />
        <RoleList
          legend="Clip editor roles"
          help="Members with these roles only see the clip pages (Clips and Clip board)."
          roles={roles}
          selected={clipEditorIds}
          onChange={setClipEditorIds}
        />
        <RoleList
          legend="Video editor roles"
          help="Members with these roles only see the queue, pipeline and series."
          roles={roles}
          selected={videoEditorIds}
          onChange={setVideoEditorIds}
        />

        <label>
          Allowed user IDs
          <textarea
            rows={4}
            value={allowed}
            placeholder="Discord user IDs, separated by commas, spaces or new lines"
            onChange={(e) => setAllowed(e.target.value)}
          />
        </label>

        <label>
          Suggestions channel
          {channels.error && <ErrorBox message={channels.error} onRetry={channels.reload} />}
          <select value={channel} onChange={(e) => setChannel(e.target.value)}>
            <option value="">Disabled</option>
            {channel && channels.data && !knownChannel && <option value={channel}>Unknown channel {channel}</option>}
            {channel && !channels.data && <option value={channel}>Current channel ({channel})</option>}
            {channels.data?.map((c) => (
              <option key={c.id} value={c.id}>
                #{c.name}
              </option>
            ))}
          </select>
        </label>

        <label>
          Clips channel
          <select value={clipsChannel} onChange={(e) => setClipsChannel(e.target.value)}>
            <option value="">Disabled</option>
            {clipsChannel && channels.data && !knownClipsChannel && (
              <option value={clipsChannel}>Unknown channel {clipsChannel}</option>
            )}
            {clipsChannel && !channels.data && <option value={clipsChannel}>Current channel ({clipsChannel})</option>}
            {channels.data?.map((c) => (
              <option key={c.id} value={c.id}>
                #{c.name}
              </option>
            ))}
          </select>
        </label>
        <p className="muted small">Twitch clip links shared here show up in the Clips inbox. Must differ from the suggestions channel.</p>

        <label className="check">
          <input type="checkbox" checked={blockSelf} onChange={(e) => setBlockSelf(e.target.checked)} />
          Automatically discard self-suggestions
        </label>
        <p className="muted small">
          When a suggester's Discord name matches the YouTube channel of the video they suggest. Requires YouTube
          lookups. You can still restore them.
        </p>

        {error && <ErrorBox message={error} />}
        {notice && <div className="notice">{notice}</div>}
        <div className="actions">
          <button type="submit" className="primary" disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>

      <h2>Danger zone</h2>
      <div className="card">
        <p>
          Deleting this server permanently deletes all of its queue, series and suggestions data. This cannot be
          undone.
        </p>
        <label className="form">
          <span className="muted small">
            Type <strong>{server.name}</strong> to confirm
          </span>
          <input value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" />
        </label>
        {deleteError && <ErrorBox message={deleteError} />}
        <div className="actions">
          <button type="button" className="danger" disabled={deleting || confirm !== server.name} onClick={onDelete}>
            {deleting ? 'Deleting…' : 'Delete server'}
          </button>
        </div>
      </div>
    </>
  )
}
