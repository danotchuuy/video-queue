import { useCallback, useEffect, useRef, useState } from 'react'
import { errMsg } from './util'

export interface FetchState<T> {
  data: T | null
  error: string | null
  loading: boolean
  reload: () => void
}

interface Result<T> {
  token: string
  data: T | null
  error: string | null
}

/** Runs `fn` on mount and whenever `key` changes; `reload` refetches. Keeps old data while reloading. */
export function useFetch<T>(fn: () => Promise<T>, key: string = ''): FetchState<T> {
  const [tick, setTick] = useState(0)
  const [result, setResult] = useState<Result<T> | null>(null)
  const fnRef = useRef(fn)
  const token = `${key}#${tick}`

  useEffect(() => {
    fnRef.current = fn
  })

  useEffect(() => {
    let cancelled = false
    fnRef.current().then(
      (data) => !cancelled && setResult({ token, data, error: null }),
      (e) => !cancelled && setResult((r) => ({ token, data: r?.data ?? null, error: errMsg(e) })),
    )
    return () => {
      cancelled = true
    }
  }, [token])

  const reload = useCallback(() => setTick((t) => t + 1), [])
  return {
    data: result?.data ?? null,
    error: result?.error ?? null,
    loading: result?.token !== token,
    reload,
  }
}
