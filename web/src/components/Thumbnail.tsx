import { formatDuration, youtubeThumb } from '../util'

interface Props {
  videoId: string
  title: string
  seconds?: number
  size?: 'sm' | 'md' | 'lg' | 'fill'
}

/** 16:9 thumbnail derived from a YouTube video ID; renders nothing when the ID is absent or invalid. */
export default function Thumbnail({ videoId, title, seconds = 0, size = 'md' }: Props) {
  const src = youtubeThumb(videoId)
  if (!src) return null
  const dur = formatDuration(seconds)
  return (
    <div className={size === 'md' ? 'thumb' : `thumb thumb-${size}`}>
      <img
        src={src}
        alt={title}
        loading="lazy"
        decoding="async"
        referrerPolicy="no-referrer"
        onError={(e) => (e.currentTarget.style.visibility = 'hidden')}
      />
      {dur && <span className="thumb-badge">{dur}</span>}
    </div>
  )
}
