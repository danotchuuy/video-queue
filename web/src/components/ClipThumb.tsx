import { formatDuration } from '../util'

interface Props {
  src: string
  title: string
  seconds?: number
  size?: 'sm' | 'md' | 'lg' | 'fill'
}

/** 16:9 thumbnail for a Twitch clip; renders nothing without an image URL. */
export default function ClipThumb({ src, title, seconds = 0, size = 'md' }: Props) {
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
