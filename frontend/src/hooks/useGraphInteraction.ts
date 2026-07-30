import { useRef, useCallback } from 'react'
import type { SimNode, GraphEdge } from '../types/graph'
import { nodeMatch } from '../utils/search'

export interface GraphInteractionResult {
  handleNodeEnter: (id: string) => void
  handleNodeLeave: () => void
  handleNodeClick: (id: string) => boolean
  setSearchQuery: (query: string) => void
  getActiveId: () => string | null
  getPinnedId: () => string | null
  pinNode: (id: string) => void
  clearAll: () => void
  clearHover: () => void
  nodeOpacityRule: (nodeId: string) => number
  nodeStrokeRule: (nodeId: string) => { width: number; color: string }
  linkOpacityRule: (sourceId: string, targetId: string) => number
  isInteracting: () => boolean
}

export function useGraphInteraction(
  adjacency: Map<string, GraphEdge[]>,
  nodesRef: React.MutableRefObject<SimNode[]>,
): GraphInteractionResult {
  const hoveredIdRef = useRef<string | null>(null)
  const pinnedIdRef = useRef<string | null>(null)
  const searchQueryRef = useRef('')

  const getActiveId = () => pinnedIdRef.current ?? hoveredIdRef.current
  const getPinnedId = () => pinnedIdRef.current

  const handleNodeEnter = useCallback((id: string) => { hoveredIdRef.current = id }, [])
  const handleNodeLeave = useCallback(() => { hoveredIdRef.current = null }, [])
  const handleNodeClick = useCallback((id: string) => {
    pinnedIdRef.current = pinnedIdRef.current === id ? null : id
    hoveredIdRef.current = pinnedIdRef.current
    return !!pinnedIdRef.current
  }, [])
  const setSearchQuery = useCallback((query: string) => { searchQueryRef.current = query }, [])
  const pinNode = useCallback((id: string) => {
    pinnedIdRef.current = id
    hoveredIdRef.current = id
  }, [])
  const clearAll = useCallback(() => {
    hoveredIdRef.current = null
    pinnedIdRef.current = null
  }, [])
  const clearHover = useCallback(() => { hoveredIdRef.current = null }, [])
  const isInteracting = useCallback(() => {
    return (pinnedIdRef.current ?? hoveredIdRef.current) !== null || searchQueryRef.current !== ''
  }, [])

  const nodeOpacityRule = useCallback((nodeId: string) => {
    const activeId = getActiveId()
    const query = searchQueryRef.current
    if (!activeId && !query) return 1

    if (activeId) {
      const edges = adjacency.get(activeId) || []
      const neighbors = new Set<string>()
      for (const e of edges) {
        neighbors.add(e.source === activeId ? e.target : e.source)
      }
      neighbors.add(activeId)
      return neighbors.has(nodeId) ? 1 : 0.08
    }

    const node = nodesRef.current.find(n => n.id === nodeId)
    return node && nodeMatch(node, query) ? 1 : 0.08
  }, [adjacency, nodesRef])

  const nodeStrokeRule = useCallback((nodeId: string) => {
    const activeId = getActiveId()
    if (activeId === nodeId) return { width: 4, color: '#fff' }
    return { width: 1.5, color: 'none' }
  }, [])

  const linkOpacityRule = useCallback((sourceId: string, targetId: string) => {
    const activeId = getActiveId()
    const query = searchQueryRef.current
    if (!activeId && !query) return 1
    if (activeId) {
      return sourceId === activeId || targetId === activeId ? 0.7 : 0.04
    }
    return `${sourceId}|${targetId}`.toLowerCase().includes(query) ? 0.5 : 0
  }, [])

  return {
    handleNodeEnter,
    handleNodeLeave,
    handleNodeClick,
    setSearchQuery,
    getActiveId,
    getPinnedId,
    pinNode,
    clearAll,
    clearHover,
    isInteracting,
    nodeOpacityRule,
    nodeStrokeRule,
    linkOpacityRule,
  }
}
