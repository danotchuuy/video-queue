import type { Server } from './api'

export type SectionKey = 'videos' | 'suggestions' | 'clips'

/** Server sections in default-redirect order: Queue, Suggestions, Clips. */
export const SECTIONS: { path: string; label: string; end?: boolean; key: SectionKey }[] = [
  { path: '', label: 'Queue', end: true, key: 'videos' },
  { path: '/pipeline', label: 'Pipeline', key: 'videos' },
  { path: '/series', label: 'Series', key: 'videos' },
  { path: '/suggestions', label: 'Suggestions', key: 'suggestions' },
  { path: '/clips', label: 'Clips', end: true, key: 'clips' },
  { path: '/clips/board', label: 'Clip board', key: 'clips' },
]

export function allows(server: Server, key: SectionKey): boolean {
  return server.sections?.[key] ?? false
}

/** Path (relative to /s/:id) of the first section the server allows, or null. */
export function firstAllowedPath(server: Server): string | null {
  return SECTIONS.find((s) => allows(server, s.key))?.path ?? null
}
