import type { VideoStatus } from '../api'
import { STATUS_LABELS } from '../util'

export function Loading() {
  return <p className="muted">Loading…</p>
}

export function ErrorBox({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="error" role="alert">
      {message}
      {onRetry && (
        <button type="button" className="link-btn" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  )
}

export function StatusBadge({ status }: { status: VideoStatus }) {
  return <span className={`badge badge-${status}`}>{STATUS_LABELS[status]}</span>
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { value: T; label: string }[]
  value: T
  onChange: (v: T) => void
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.value}
          type="button"
          role="tab"
          aria-selected={t.value === value}
          className={t.value === value ? 'tab active' : 'tab'}
          onClick={() => onChange(t.value)}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}
