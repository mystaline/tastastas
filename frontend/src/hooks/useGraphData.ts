import { useState, useEffect, useCallback } from 'react'
import type { GraphData } from '../types/graph'
import { getProjectFromPath } from '../utils/route'

export interface UseGraphDataResult {
  loading: boolean
  error: string | null
  data: GraphData | null
  linkedProjects: string[]
  selectedSidecars: string[]
  toggleSidecar: (p: string) => void
}

export function useGraphData(): UseGraphDataResult {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [data, setData] = useState<GraphData | null>(null)
  const [linkedProjects, setLinkedProjects] = useState<string[]>([])
  const [selectedSidecars, setSelectedSidecars] = useState<string[]>([])

  const project = getProjectFromPath()

  const stage = new URLSearchParams(window.location.search).get('stage')

  useEffect(() => {
    if (!project) {
      setError('Invalid graph URL')
      setLoading(false)
      return
    }
    const controller = new AbortController()
    const signal = controller.signal
    const stageParam = stage ? `?stage=${encodeURIComponent(stage)}` : ''
    fetch(`/api/graph/${project}/linked${stageParam}`, {
      signal,
      headers: { Accept: 'application/json' },
    })
      .then(res => (res.ok ? res.json() : null))
      .then((d: { linked_projects?: string[] } | null) => {
        if (!signal.aborted && d?.linked_projects) setLinkedProjects(d.linked_projects)
      })
      .catch(() => {})
    return () => controller.abort()
  }, [project, stage])

  useEffect(() => {
    if (!project) return
    const query = window.location.search
    const ts = Date.now()
    const sidecarParam = selectedSidecars.length
      ? `&sidecars=${encodeURIComponent(selectedSidecars.join(','))}`
      : ''
    const cacheBuster = query ? `&_=${ts}` : `?_=${ts}`
    const controller = new AbortController()
    const signal = controller.signal

    setLoading(true)
    fetch(`/api/graph/${project}${query}${sidecarParam}${cacheBuster}`, {
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
  }, [project, selectedSidecars])

  const toggleSidecar = useCallback((p: string) => {
    setSelectedSidecars(prev =>
      prev.includes(p) ? prev.filter(x => x !== p) : [...prev, p],
    )
  }, [])

  return { loading, error, data, linkedProjects, selectedSidecars, toggleSidecar }
}
