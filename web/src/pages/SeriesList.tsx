import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { createSeries, deleteSeries, listSeries, updateSeries, type Series } from '../api'
import PlaylistImportForm from '../components/PlaylistImportForm'
import SeriesForm from '../components/SeriesForm'
import { ErrorBox, Loading } from '../components/Status'
import Thumbnail from '../components/Thumbnail'
import { useServer, useSession } from '../session'
import { useFetch } from '../useFetch'
import { VENUE_LABELS, errMsg } from '../util'

export default function SeriesList() {
  const { id: sid } = useServer()
  const { me } = useSession()
  const navigate = useNavigate()
  const [fromPlaylist, setFromPlaylist] = useState(false)
  const { data, error, loading, reload } = useFetch(() => listSeries(sid), String(sid))
  const [adding, setAdding] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  if (loading && !data) return <Loading />
  if (error && !data) return <ErrorBox message={error} onRetry={reload} />

  async function remove(s: Series) {
    if (!window.confirm(`Delete series "${s.title}"?`)) return
    setActionError(null)
    try {
      await deleteSeries(sid, s.id)
      reload()
    } catch (e) {
      setActionError(errMsg(e))
    }
  }

  return (
    <>
      <div className="page-head">
        <h1>Series</h1>
        {!adding && (
          <button type="button" className="primary" onClick={() => setAdding(true)}>
            New series
          </button>
        )}
      </div>
      {adding && (
        <section className="card">
          <h2>New series</h2>
          {me.youtube_enabled && (
            <div className="mode-toggle">
              <button type="button" className={fromPlaylist ? '' : 'active'} onClick={() => setFromPlaylist(false)}>
                Manual
              </button>
              <button type="button" className={fromPlaylist ? 'active' : ''} onClick={() => setFromPlaylist(true)}>
                From YouTube playlist or show
              </button>
            </div>
          )}
          {me.youtube_enabled && fromPlaylist ? (
            <PlaylistImportForm
              onCancel={() => setAdding(false)}
              onImported={(s) => navigate(`/s/${sid}/series/${s.id}`)}
            />
          ) : (
            <SeriesForm
              submitLabel="Create"
              onCancel={() => setAdding(false)}
              onSubmit={async (input) => {
                await createSeries(sid, input)
                setAdding(false)
                reload()
              }}
            />
          )}
        </section>
      )}
      {actionError && <ErrorBox message={actionError} />}
      {(data ?? []).length === 0 && <p className="muted">No series yet.</p>}
      {(data ?? []).map((s) => (
        <article key={s.id} className="card">
          {editingId === s.id ? (
            <SeriesForm
              initial={s}
              submitLabel="Save"
              onCancel={() => setEditingId(null)}
              onSubmit={async (input) => {
                await updateSeries(sid, s.id, input)
                setEditingId(null)
                reload()
              }}
            />
          ) : (
            <div className="card-head">
              <div className="media grow">
                <Thumbnail videoId={s.thumbnail_video_id} title={s.title} />
                <div className="grow">
                <div className="card-title">
                  <Link to={`/s/${sid}/series/${s.id}`}>{s.title}</Link>
                </div>
                <div className="muted small">
                  {[s.creator, s.default_venue && VENUE_LABELS[s.default_venue]]
                    .filter(Boolean)
                    .join(' · ')}
                  {s.url && (
                    <>
                      {' · '}
                      <a href={s.url} target="_blank" rel="noreferrer noopener">
                        link
                      </a>
                    </>
                  )}
                </div>
                {s.notes && <div className="small">{s.notes}</div>}
                </div>
              </div>
              <div className="btn-group">
                <button type="button" onClick={() => setEditingId(s.id)}>
                  Edit
                </button>
                <button type="button" className="danger" onClick={() => remove(s)}>
                  Delete
                </button>
              </div>
            </div>
          )}
        </article>
      ))}
    </>
  )
}
