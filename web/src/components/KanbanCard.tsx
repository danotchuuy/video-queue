import type { DragEvent, KeyboardEvent } from 'react'
import type { Series, Video } from '../api'
import Thumbnail from './Thumbnail'

interface Props {
  video: Video
  column?: string
  series?: Series
  selected: boolean
  dragging: boolean
  onOpen: () => void
  onDragStart: (e: DragEvent<HTMLDivElement>) => void
  onDragEnd: () => void
}

/** Compact draggable card for the pipeline board; click, Enter or Space opens the details. */
export default function KanbanCard({
  video,
  column,
  series,
  selected,
  dragging,
  onOpen,
  onDragStart,
  onDragEnd,
}: Props) {
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onOpen()
    }
  }

  const sub = [series?.title, video.episode && `#${video.episode}`].filter(Boolean).join(' ')
  const cls = ['kanban-card', selected && 'selected', dragging && 'dragging'].filter(Boolean).join(' ')

  return (
    <div
      className={cls}
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      draggable
      onClick={onOpen}
      onKeyDown={onKeyDown}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
    >
      <Thumbnail videoId={video.youtube_video_id} title="" seconds={video.duration_seconds} size="fill" />
      <div className="kanban-title">{video.title}</div>
      {sub && <div className="muted small kanban-sub">{sub}</div>}
      {video.editor_name && <div className="muted small kanban-sub">✎ {video.editor_name}</div>}
      {column === 'posted' && (
        <div className="pipeline-platforms">
          {[
            { short: 'P', label: 'Patreon', on: video.patreon_url !== '' },
            { short: 'YT', label: 'YouTube', on: video.youtube_url !== '' },
          ].map((p) => (
            <span key={p.label} className={p.on ? 'pipeline-platform on' : 'pipeline-platform'} title={p.label}>
              {p.short}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
