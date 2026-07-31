import { useRef, useCallback } from 'react'
import type * as d3 from 'd3'
import type { GraphEdge, SimLink, SimNode } from '../types/graph'

const BATCH_SIZE = 500

export interface ProposedBatchResult {
  injectedEdgesRef: React.MutableRefObject<SimLink[]>
  scheduleBatch: (
    layerRef: React.MutableRefObject<d3.Selection<SVGGElement, unknown, null, undefined> | null>,
    afterInject: () => void,
  ) => void
  updatePositions: (
    selection: d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null,
    nodeIndex: Map<string, SimNode>,
  ) => void
  stats: { loaded: number; total: number }
}

export function useProposedBatch(proposedEdges: GraphEdge[]): ProposedBatchResult {
  const injectedEdgesRef = useRef<SimLink[]>([])
  const proposedQueueRef = useRef<SimLink[]>([])
  const idleHandleRef = useRef<number | null>(null)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const layerRefRef = useRef<React.MutableRefObject<d3.Selection<SVGGElement, unknown, null, undefined> | null> | null>(null)
  const afterInjectRef = useRef<(() => void) | null>(null)

  const scheduleBatch = useCallback((
    layerRef: React.MutableRefObject<d3.Selection<SVGGElement, unknown, null, undefined> | null>,
    afterInject: () => void,
  ) => {
    let keySeq = 0
    proposedQueueRef.current = proposedEdges.map(e => ({
      ...e,
      source: e.source,
      target: e.target,
      _key: `${e.source}|${e.target}|${e.edge_type}|${keySeq++}`,
    })) as SimLink[]

    injectedEdgesRef.current = []
    layerRefRef.current = layerRef
    afterInjectRef.current = afterInject

    const idleBatch = (deadline: IdleDeadline | null) => {
      let processed = 0
      const q = proposedQueueRef.current
      while (q.length > 0 && processed < BATCH_SIZE && (deadline ? deadline.timeRemaining() > 1 : true)) {
        injectedEdgesRef.current.push(q.shift()!)
        processed++
      }

      if (processed > 0) {
        afterInjectRef.current?.()
      }

      if (q.length > 0) {
        if (typeof requestIdleCallback !== 'undefined') {
          idleHandleRef.current = requestIdleCallback(idleBatch, { timeout: 1000 }) as unknown as number
        } else {
          idleHandleRef.current = window.setTimeout(() => idleBatch(null), 100) as unknown as number
        }
      }
    }

    timeoutRef.current = setTimeout(() => {
      if (typeof requestIdleCallback !== 'undefined') {
        idleHandleRef.current = requestIdleCallback(idleBatch, { timeout: 1000 }) as unknown as number
      } else {
        idleHandleRef.current = window.setTimeout(() => idleBatch(null), 100) as unknown as number
      }
    }, 600)
  }, [proposedEdges])

  const updatePositions = useCallback((
    selection: d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null,
    nodeIndex: Map<string, SimNode>,
  ) => {
    if (!selection) return
    selection
      .attr('x1', d => {
        const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
        return nodeIndex.get(sid)?.x ?? 0
      })
      .attr('y1', d => {
        const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
        return nodeIndex.get(sid)?.y ?? 0
      })
      .attr('x2', d => {
        const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
        return nodeIndex.get(tid)?.x ?? 0
      })
      .attr('y2', d => {
        const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
        return nodeIndex.get(tid)?.y ?? 0
      })
  }, [])

  return { injectedEdgesRef, scheduleBatch, updatePositions, stats: { loaded: 0, total: proposedEdges.length } }
}
