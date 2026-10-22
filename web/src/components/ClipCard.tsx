import type { DragEvent, KeyboardEvent } from 'react'
import { PLATFORMS, type Clip } from '../api'
import { formatDuration } from '../util'

const SHORT: Record<string, string> = { youtube: 'YT', instagram: 'IG', x: 'X', tiktok: 'TT' }

/** 16:9 clip thumbnail with a duration badge; renders nothing without a thumbnail URL. */
export function ClipImage({ clip }: { clip: Clip }) {
  if (!clip.thumbnail_url) return null
  const dur = formatDuration(clip.duration_seconds)
  return (
    <div className="clip-thumb">
      <img
        src={clip.thumbnail_url}
        alt=""
        loading="lazy"
        decoding="async"
        referrerPolicy="no-referrer"
        onError={(e) => (e.currentTarget.style.visibility = 'hidden')}
      />
      {dur && <span className="thumb-badge">{dur}</span>}
    </div>
  )
}

interface Props {
  clip: Clip
  selected: boolean
  dragging: boolean
  onOpen: () => void
  onDragStart: (e: DragEvent<HTMLDivElement>) => void
  onDragEnd: () => void
}

/** Compact draggable card for the clip board; click, Enter or Space opens the details. */
export default function ClipCard({ clip, selected, dragging, onOpen, onDragStart, onDragEnd }: Props) {
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onOpen()
    }
  }

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
      <ClipImage clip={clip} />
      <div className="kanban-title">{clip.edited_title || clip.title || 'Untitled clip'}</div>
      {clip.clipper_name && <div className="muted small kanban-sub">clipped by {clip.clipper_name}</div>}
      {clip.status === 'posted' && (
        <div className="clip-platforms">
          {PLATFORMS.map((p) => {
            const on = clip[p.field] !== ''
            return (
              <span
                key={p.id}
                className={on ? 'clip-platform on' : 'clip-platform'}
                title={on ? `Posted on ${p.label}` : `Not on ${p.label}`}
              >
                {SHORT[p.id]}
              </span>
            )
          })}
        </div>
      )}
    </div>
  )
}
