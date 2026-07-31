import { useState, useEffect } from 'react'
import type { GraphData } from '../types/graph'

export interface UseGraphDataResult {
  loading: boolean
  error: string | null
  data: GraphData | null
}

export function useGraphData(): UseGraphDataResult {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [data, setData] = useState<GraphData | null>(null)

  useEffect(() => {
    const m = window.location.pathname.match(/\/graph\/([^/]+)/)
    if (!m) {
      setError('Invalid graph URL')
      setLoading(false)
      return
    }

    const project = m[1]
    const query = window.location.search
    const ts = Date.now()
    const cacheBuster = query ? `&_=${ts}` : `?_=${ts}`
    const controller = new AbortController()
    const signal = controller.signal

    fetch(`/api/graph/${project}${query}${cacheBuster}`, {
      signal,
      headers: { Accept: 'application/json' },
    })
      .then(res => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json() as Promise<GraphData>
      })
      .then(d => {
        if (!signal.aborted) { setData(d); setLoading(false) }
      })
      .catch(err => {
        if (!signal.aborted) {
          setError(err instanceof Error ? err.message : 'Fetch failed')
          setLoading(false)
        }
      })

    return () => controller.abort()
  }, [])

  return { loading, error, data }
}
