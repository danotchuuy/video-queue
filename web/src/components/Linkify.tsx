import { URL_RE } from '../util'

/** Renders text with http(s) URLs as links. */
export default function Linkify({ text }: { text: string }) {
  const parts: React.ReactNode[] = []
  let last = 0
  for (const m of text.matchAll(URL_RE)) {
    const i = m.index
    if (i > last) parts.push(text.slice(last, i))
    parts.push(
      <a key={i} href={m[0]} target="_blank" rel="noreferrer noopener">
        {m[0]}
      </a>,
    )
    last = i + m[0].length
  }
  if (last < text.length) parts.push(text.slice(last))
  return <>{parts}</>
}
