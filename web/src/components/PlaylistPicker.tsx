import type { YouTubePlaylistItem } from '../api'
import { formatDuration } from '../util'
import Thumbnail from './Thumbnail'

interface Props {
  items: YouTubePlaylistItem[]
  selected: Set<string>
  onChange: (next: Set<string>) => void
  disabled?: boolean
}

/** Scrollable checkbox list of playlist items with select all/none. */
export default function PlaylistPicker({ items, selected, onChange, disabled }: Props) {
  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    onChange(next)
  }
  return (
    <div>
      <div className="actions">
        <button type="button" disabled={disabled} onClick={() => onChange(new Set(items.map((i) => i.id)))}>
          Select all
        </button>
        <button type="button" disabled={disabled} onClick={() => onChange(new Set())}>
          Select none
        </button>
        <span className="muted small">
          {selected.size} of {items.length} selected
        </span>
      </div>
      <ul className="preview-list">
        {items.map((it) => (
          <li key={`${it.position}-${it.id}`}>
            <label className="check">
              <input
                type="checkbox"
                checked={selected.has(it.id)}
                disabled={disabled}
                onChange={() => toggle(it.id)}
              />
              <span className="pos muted small">{it.position}</span>
              <Thumbnail videoId={it.id} title={it.title} size="sm" />
              <span className="grow">{it.title}</span>
              <span className="muted small">{formatDuration(it.duration_seconds)}</span>
            </label>
          </li>
        ))}
      </ul>
    </div>
  )
}
