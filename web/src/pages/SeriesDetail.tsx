import { useState } from 'react'
import { Link, useParams } from 'react-router'
import {
  importIntoSeries,
  listSeries,
  listVideos,
  lookupPlaylist,
  type YouTubePlaylistItem,
} from '../api'
import PlaylistPicker from '../components/PlaylistPicker'
import { ErrorBox, Loading, StatusBadge } from '../components/Status'
import Thumbnail from '../components/Thumbnail'
import { useServer, useSession } from '../session'
import { useFetch } from '../useFetch'
import { VENUE_LABELS, errMsg, formatDate } from '../util'

export default function SeriesDetail() {
  const { id: sid } = useServer()
  const id = Number(useParams().id)
  const { me } = useSession()
  const [checking, setChecking] = useState(false)
  const [newItems, setNewItems] = useState<YouTubePlaylistItem[] | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [importing, setImporting] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const series = useFetch(() => listSeries(sid), String(sid))
  const videos = useFetch(() => listVideos(sid, { series_id: id }), `${sid}/${id}`)

  if ((series.loading && !series.data) || (videos.loading && !videos.data)) return <Loading />
  const err = series.error ?? videos.error
  if (err) return <ErrorBox message={err} onRetry={() => (series.reload(), videos.reload())} />

  const s = series.data?.find((x) => x.id === id)
  if (!s) return <p>Series not found. <Link to={`/s/${sid}/series`}>Back to series</Link></p>
  const list = [...(videos.data ?? [])].sort((a, b) => a.position - b.position)

  async function checkPlaylist() {
    setChecking(true)
    setCheckError(null)
    setNotice(null)
    setNewItems(null)
    try {
      const have = new Set(list.map((v) => v.youtube_video_id).filter(Boolean))
      const p = await lookupPlaylist(`https://www.youtube.com/playlist?list=${s!.youtube_playlist_id}`)
      const fresh = p.items.filter((i) => !have.has(i.id))
      if (fresh.length === 0) setNotice('No new videos')
      setNewItems(fresh)
      setSelected(new Set(fresh.map((i) => i.id)))
    } catch (e) {
      setCheckError(errMsg(e))
    } finally {
      setChecking(false)
    }
  }

  async function importChecked() {
    if (!newItems) return
    setImporting(true)
    setCheckError(null)
    try {
      const res = await importIntoSeries(sid, id, {
        video_ids: newItems.filter((i) => selected.has(i.id)).map((i) => i.id),
      })
      setNewItems(null)
      setNotice(`Imported ${res.videos.length} video${res.videos.length === 1 ? '' : 's'}`)
      series.reload()
      videos.reload()
    } catch (e) {
      setCheckError(errMsg(e))
    } finally {
      setImporting(false)
    }
  }

  return (
    <>
      <p className="small">
        <Link to={`/s/${sid}/series`}>← All series</Link>
      </p>
      <div className="media">
        <Thumbnail videoId={s.thumbnail_video_id} title={s.title} size="lg" />
        <div className="grow">
      <h1>{s.title}</h1>
      <p className="muted">
        {[s.creator, s.default_venue && VENUE_LABELS[s.default_venue]].filter(Boolean).join(' · ')}
        {s.url && (
          <>
            {' · '}
            <a href={s.url} target="_blank" rel="noreferrer noopener">
              link
            </a>
          </>
        )}
      </p>
      {s.notes && <p>{s.notes}</p>}
        </div>
      </div>
      {me.youtube_enabled && s.youtube_playlist_id && (
        <section className="card">
          <div className="actions">
            <button type="button" disabled={checking || importing} onClick={checkPlaylist}>
              {checking ? 'Checking…' : 'Check playlist for new videos'}
            </button>
          </div>
          {checkError && <ErrorBox message={checkError} />}
          {notice && <div className="notice">{notice}</div>}
          {newItems && newItems.length > 0 && (
            <>
              <PlaylistPicker items={newItems} selected={selected} onChange={setSelected} disabled={importing} />
              <div className="actions">
                <button
                  type="button"
                  className="primary"
                  disabled={importing || selected.size === 0}
                  onClick={importChecked}
                >
                  {importing ? 'Importing…' : `Import ${selected.size} video${selected.size === 1 ? '' : 's'}`}
                </button>
                <button type="button" disabled={importing} onClick={() => setNewItems(null)}>
                  Cancel
                </button>
              </div>
            </>
          )}
        </section>
      )}
      {list.length === 0 ? (
        <p className="muted">No videos in this series yet.</p>
      ) : (
        <ul className="plain">
          {list.map((v) => (
            <li key={v.id} className="card card-row">
              <Thumbnail videoId={v.youtube_video_id} title={v.title} seconds={v.duration_seconds} />
              <div className="grow">
                {v.episode && <span className="muted">#{v.episode} </span>}
                {v.url ? (
                  <a href={v.url} target="_blank" rel="noreferrer noopener">
                    {v.title}
                  </a>
                ) : (
                  v.title
                )}
                <div className="muted small">
                  {VENUE_LABELS[v.venue]}
                  {v.scheduled_for && ` · scheduled ${formatDate(v.scheduled_for)}`}
                </div>
                {v.suggested_by_name && <div className="muted small">Suggested by {v.suggested_by_name}</div>}
              </div>
              <StatusBadge status={v.status} />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}
