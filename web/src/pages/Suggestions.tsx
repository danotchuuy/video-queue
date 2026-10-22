import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import {
  approveSuggestion,
  discardSuggestion,
  listSeries,
  listSuggestions,
  removeSuggestions,
  restoreSuggestion,
  syncSuggestions,
  unremoveSuggestion,
  type Suggestion,
  type SuggestionSort,
  type SuggestionStatus,
} from '../api'
import Linkify from '../components/Linkify'
import { ErrorBox, Loading, Tabs } from '../components/Status'
import Thumbnail from '../components/Thumbnail'
import VideoForm from '../components/VideoForm'
import { useServer } from '../session'
import { useFetch } from '../useFetch'
import { errMsg, firstUrl, firstYouTubeId, formatDateTime, formatDuration } from '../util'
import { emptyValues, toCreate } from '../videoValues'

const TABS: { value: SuggestionStatus; label: string }[] = [
  { value: 'pending', label: 'Pending' },
  { value: 'approved', label: 'Approved' },
  { value: 'discarded', label: 'Discarded' },
]

function titleGuess(content: string, url: string): string {
  const t = (url ? content.replace(url, '') : content).replace(/\s+/g, ' ').trim()
  return t.slice(0, 300)
}

export default function Suggestions() {
  const server = useServer()
  const sid = server.id
  const [status, setStatus] = useState<SuggestionStatus>('pending')
  const [sort, setSort] = useState<SuggestionSort>('newest')
  const [approvingId, setApprovingId] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [undoId, setUndoId] = useState<number | null>(null)
  const [expandedIds, setExpandedIds] = useState<number[]>([])
  const [hiddenIds, setHiddenIds] = useState<number[]>([])
  const [searchParams, setSearchParams] = useSearchParams()
  const authorId = searchParams.get('author') ?? ''

  const list = useFetch(
    () => listSuggestions(sid, status, sort, authorId || undefined),
    `${sid}/${status}/${sort}/${authorId}`,
  )
  const series = useFetch(() => listSeries(sid), String(sid))

  async function act(fn: () => Promise<unknown>, done?: string) {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    setUndoId(null)
    try {
      await fn()
      if (done) setNotice(done)
      list.reload()
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  function clearUndo() {
    if (undoId === null) return
    setUndoId(null)
    setNotice(null)
  }

  function changeStatus(next: SuggestionStatus) {
    clearUndo()
    setStatus(next)
  }

  async function removeOne(id: number) {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    setUndoId(null)
    setHiddenIds((h) => [...h, id])
    try {
      await removeSuggestions(sid, [id])
      setNotice('Removed.')
      setUndoId(id)
      list.reload()
    } catch (e) {
      setHiddenIds((h) => h.filter((x) => x !== id))
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  async function undoRemove() {
    if (undoId === null) return
    const id = undoId
    setBusy(true)
    setActionError(null)
    try {
      await unremoveSuggestion(sid, id)
      setNotice(null)
      setUndoId(null)
      setHiddenIds((h) => h.filter((x) => x !== id))
      list.reload()
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  function removeAll(items: Suggestion[]) {
    const n = items.length
    const noun = n === 1 ? 'suggestion' : 'suggestions'
    if (!window.confirm(`Remove ${n} ${noun} from this list? They won't come back on sync. Approved videos stay in the queue.`)) {
      return
    }
    const ids = items.map((s) => s.id)
    act(async () => {
      const { removed } = await removeSuggestions(sid, ids)
      setHiddenIds((h) => [...h, ...ids])
      setNotice(`Removed ${removed} ${removed === 1 ? 'suggestion' : 'suggestions'}.`)
    })
  }

  function setAuthor(id: string) {
    clearUndo()
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (id) next.set('author', id)
      else next.delete('author')
      return next
    })
  }

  function renderCard(s: Suggestion) {
    const guessUrl = firstUrl(s.content)
    const url = s.youtube_video_id ? `https://www.youtube.com/watch?v=${s.youtube_video_id}` : guessUrl
    const thumbId = s.youtube_video_id || firstYouTubeId(s.content)
    const guess = titleGuess(s.content, guessUrl)
    const heading = s.video_title.trim()
    const channel = [s.channel_title, s.channel_handle].filter(Boolean).join(' ')
    const details = [channel, formatDuration(s.duration_seconds)].filter(Boolean).join(' · ')
    const suggesters = s.suggesters ?? []
    const others = suggesters.filter((p) => p.author_id !== s.author_id)
    const expanded = expandedIds.includes(s.id)
    const shownOthers = expanded ? others : others.slice(0, 5)
    const countBadge = suggesters.length > 1 && (
      <span className="badge" title={`Suggested by ${suggesters.length} people`}>
        ×{suggesters.length}
      </span>
    )
    const alsoBy = suggesters.length > 1 && others.length > 0 && (
      <div className="muted small">
        Also suggested by{' '}
        {shownOthers.map((p, i) => (
          <span key={p.author_id}>
            {i > 0 && ', '}
            <button
              type="button"
              className="link-btn inline"
              title="Show only suggestions by this person"
              onClick={() => setAuthor(p.author_id)}
            >
              {p.author_name}
            </button>
          </span>
        ))}
        {others.length > 5 && !expanded && (
          <>
            {', '}
            <button
              type="button"
              className="link-btn inline"
              onClick={() => setExpandedIds((x) => [...x, s.id])}
            >
              +{others.length - 5} more
            </button>
          </>
        )}
      </div>
    )
    const byline = (
      <div className="muted small">
        <button
          type="button"
          className="author-btn"
          title="Show only suggestions by this person"
          onClick={() => setAuthor(s.author_id)}
        >
          <strong>{s.author_name}</strong>
        </button>{' '}
        · {formatDateTime(s.posted_at)} · {s.reaction_count}{' '}
        {s.reaction_count === 1 ? 'reaction' : 'reactions'}
        {s.self_suggestion && (
          <>
            {' · '}
            <span
              className="badge badge-warning"
              title="The suggester's Discord name matches the YouTube channel"
            >
              Self-promo?
            </span>
          </>
        )}
        {s.status === 'discarded' && s.discard_reason === 'self_suggestion' && ' · Auto-discarded: self-suggestion'}
      </div>
    )
    return (
      <article key={s.id} className="card">
        {heading ? (
          <>
            <div className="media">
              <Thumbnail videoId={thumbId} title={heading} seconds={s.duration_seconds} />
              <div className="grow">
                <div className="card-title">
                  {heading} {countBadge}
                </div>
                {details && <div className="muted small">{details}</div>}
              </div>
            </div>
            <p className="content muted small">
              <Linkify text={s.content} />
            </p>
            {byline}
            {alsoBy}
          </>
        ) : (
          <>
            {countBadge && <div>{countBadge}</div>}
            {byline}
            {alsoBy}
            <div className="media">
              <Thumbnail videoId={thumbId} title={guess || 'YouTube video'} />
              <p className="content grow">
                <Linkify text={s.content} />
              </p>
            </div>
          </>
        )}
        {approvingId === s.id ? (
          <VideoForm
            series={series.data ?? []}
            initial={{
              ...emptyValues(),
              url,
              title: heading || guess,
              channel_title: s.channel_title,
              duration_seconds: s.duration_seconds,
            }}
            submitLabel="Approve"
            onCancel={() => setApprovingId(null)}
            onSubmit={async (v) => {
              await approveSuggestion(sid, s.id, toCreate(v))
              setApprovingId(null)
              setNotice('Approved and added to the queue.')
              list.reload()
            }}
          />
        ) : (
          <div className="actions">
            {s.status === 'pending' && (
              <>
                <button type="button" className="primary" disabled={busy} onClick={() => setApprovingId(s.id)}>
                  Approve
                </button>
                <button type="button" disabled={busy} onClick={() => act(() => discardSuggestion(sid, s.id))}>
                  Discard
                </button>
              </>
            )}
            {s.status === 'discarded' && (
              <button type="button" disabled={busy} onClick={() => act(() => restoreSuggestion(sid, s.id))}>
                Restore
              </button>
            )}
            {s.status === 'approved' && <span className="muted small">Approved by {s.reviewed_by || 'a mod'}</span>}
            <button type="button" className="quiet" disabled={busy} onClick={() => removeOne(s.id)}>
              Remove
            </button>
          </div>
        )}
      </article>
    )
  }

  const items = list.data?.filter((s) => !hiddenIds.includes(s.id))
  const canRemoveAll = status !== 'pending' && !!items && items.length > 0
  const hasChannel = !!server.suggestions_channel_id

  return (
    <>
      <div className="page-head">
        <h1>Suggestions</h1>
        <div className="btn-group">
          <select aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value as SuggestionSort)}>
            <option value="newest">Newest</option>
            <option value="oldest">Oldest</option>
            <option value="votes">Most reactions</option>
          </select>
          {hasChannel && (
            <button
              type="button"
              disabled={busy}
              onClick={() => act(() => syncSuggestions(sid), 'Sync finished.')}
            >
              Sync now
            </button>
          )}
        </div>
      </div>
      {!hasChannel && (
        <p className="muted">
          Suggestions aren't synced for this server.{' '}
          {server.can_manage ? (
            <>
              Choose a channel in <Link to={`/manage/servers/${sid}`}>server settings</Link>.
            </>
          ) : (
            'Ask an admin to choose a suggestions channel.'
          )}
        </p>
      )}
      {authorId && (
        <div className="filter-head">
          <h2>Suggestions by {list.data?.[0]?.author_name ?? authorId}</h2>
          <button type="button" aria-label="Clear author filter" onClick={() => setAuthor('')}>
            ×
          </button>
        </div>
      )}
      <Tabs tabs={TABS} value={status} onChange={changeStatus} />
      {notice && (
        <div className="notice">
          {notice}
          {undoId !== null && (
            <button type="button" className="link-btn" disabled={busy} onClick={undoRemove}>
              Undo
            </button>
          )}
        </div>
      )}
      {actionError && <ErrorBox message={actionError} />}
      {list.error && <ErrorBox message={list.error} onRetry={list.reload} />}
      {list.loading && !list.data && <Loading />}
      {items && items.length === 0 && <p className="muted">No {status} suggestions.</p>}
      {canRemoveAll && (
        <div className="list-head">
          <button type="button" className="quiet" disabled={busy} onClick={() => removeAll(items)}>
            Remove all {items.length}
          </button>
        </div>
      )}
      {items?.map(renderCard)}
    </>
  )
}
