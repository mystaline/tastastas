import { useState, useEffect } from 'react'
import type { GraphData } from '../types/graph'

export function useGraphData() {
  const [data, setData] = useState<GraphData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const m = window.location.pathname.match(/\/graph\/([^/]+)/)
    if (!m) {
      setError('Invalid graph URL: ' + window.location.pathname)
      setLoading(false)
      return
    }
    const project = m[1]
    const query = window.location.search
    const ts = Date.now()
    const cacheBuster = query ? `&_=${ts}` : `?_=${ts}`
    fetch(`/api/graph/${project}${query}${cacheBuster}`, {
      headers: { Accept: 'application/json' },
    })
      .then(res => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json() as Promise<GraphData>
      })
      .then(d => { setData(d); setLoading(false) })
      .catch(err => { setError(err.message); setLoading(false) })
  }, [])

  return { data, loading, error }
}
