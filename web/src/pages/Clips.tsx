import { useState } from 'react'
import { Link } from 'react-router'
import { listClips, syncClips, updateClip, type Clip, type ClipSort, type ClipStatus } from '../api'
import ClipThumb from '../components/ClipThumb'
import { ErrorBox, Loading, Tabs } from '../components/Status'
import { useServer, useSession } from '../session'
import { useFetch } from '../useFetch'
import { errMsg, formatDateTime } from '../util'

type Tab = 'new' | 'discarded'

const TABS: { value: Tab; label: string }[] = [
  { value: 'new', label: 'New' },
  { value: 'discarded', label: 'Discarded' },
]

export default function Clips() {
  const server = useServer()
  const sid = server.id
  const [status, setStatus] = useState<Tab>('new')
  const [sort, setSort] = useState<ClipSort>('newest')
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [hiddenIds, setHiddenIds] = useState<number[]>([])

  const list = useFetch(() => listClips(sid, { status: [status], sort }), `${sid}/${status}/${sort}`)

  async function sync() {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      await syncClips(sid)
      setNotice('Sync finished.')
      list.reload()
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  async function move(id: number, to: ClipStatus) {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    setHiddenIds((h) => [...h, id])
    try {
      await updateClip(sid, id, { status: to })
      list.reload()
    } catch (e) {
      setHiddenIds((h) => h.filter((x) => x !== id))
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  function renderCard(c: Clip) {
    const title = c.title.trim() || 'Untitled clip'
    return (
      <article key={c.id} className="card">
        <div className="media">
          <ClipThumb src={c.thumbnail_url} title={title} seconds={c.duration_seconds} />
          <div className="grow">
            <div className="card-title">
              <a href={c.clip_url} target="_blank" rel="noreferrer">
                {title}
              </a>
            </div>
            {c.clipper_name && <div className="muted small">Clipped by {c.clipper_name}</div>}
            <div className="muted small">
              shared by <strong>{c.author_name}</strong> · {formatDateTime(c.posted_at)}
            </div>
            {c.broadcaster_name && <div className="muted small">Broadcaster: {c.broadcaster_name}</div>}
            {c.message_url && (
              <div className="small">
                <a href={c.message_url} target="_blank" rel="noreferrer">
                  Discord message
                </a>
              </div>
            )}
          </div>
        </div>
        <div className="actions">
          {c.status === 'new' && (
            <>
              <button type="button" className="primary" disabled={busy} onClick={() => move(c.id, 'pending')}>
                Select
              </button>
              <button type="button" disabled={busy} onClick={() => move(c.id, 'discarded')}>
                Discard
              </button>
            </>
          )}
          {c.status === 'discarded' && (
            <button type="button" disabled={busy} onClick={() => move(c.id, 'new')}>
              Restore
            </button>
          )}
        </div>
      </article>
    )
  }

  const items = list.data?.filter((c) => !hiddenIds.includes(c.id))
  const hasChannel = !!server.clips_channel_id
  const { me } = useSession()

  return (
    <>
      <div className="page-head">
        <h1>Clips</h1>
        <div className="btn-group">
          <select aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value as ClipSort)}>
            <option value="newest">Newest</option>
            <option value="oldest">Oldest</option>
          </select>
          {hasChannel && (
            <button type="button" disabled={busy} onClick={sync}>
              Sync now
            </button>
          )}
        </div>
      </div>
      {hasChannel && !me.twitch_enabled && (
        <p className="muted">
          Twitch lookups are off, so clips show without their Twitch title, clipper and thumbnail. The server needs{' '}
          <code>TWITCH_CLIENT_ID</code> and <code>TWITCH_CLIENT_SECRET</code>; existing clips are filled in on the next
          sync after they're set.
        </p>
      )}
      {!hasChannel && (
        <p className="muted">
          Clips aren't synced for this server.{' '}
          {server.can_manage ? (
            <>
              Choose a channel in <Link to={`/manage/servers/${sid}`}>server settings</Link>.
            </>
          ) : (
            'Ask an admin to choose a clips channel.'
          )}
        </p>
      )}
      <Tabs
        tabs={TABS}
        value={status}
        onChange={(t) => {
          setHiddenIds([])
          setStatus(t)
        }}
      />
      {notice && <div className="notice">{notice}</div>}
      {actionError && <ErrorBox message={actionError} />}
      {list.error && <ErrorBox message={list.error} onRetry={list.reload} />}
      {list.loading && !list.data && <Loading />}
      {items && items.length === 0 && <p className="muted">No {status} clips.</p>}
      {items?.map(renderCard)}
    </>
  )
}
