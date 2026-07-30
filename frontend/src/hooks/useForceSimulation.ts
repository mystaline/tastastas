import { useRef, useState, useEffect, useCallback } from 'react'
import * as d3 from 'd3'
import type { GraphNode, GraphEdge, GraphData } from '../types/graph'
import { makeGroupHues, findRootId, nodeR, nodeColor } from '../utils/colors'
import {
  computeGroupNames,
  computeAngleStep,
  computeOrbitalParams,
  computeOrbitalTarget,
} from '../utils/layout'

type SimNode = GraphNode & {
  x: number
  y: number
  vx: number
  vy: number
  fx: number | null
  fy: number | null
}

interface SimLink extends d3.SimulationLinkDatum<SimNode> {
  edge_type: string
  confidence: number
  _key?: string
}

export interface TooltipState {
  type: 'node' | 'edge'
  data: GraphNode | GraphEdge
  connectedEdges: GraphEdge[]
  x: number
  y: number
  visible: boolean
}

export interface GraphStats {
  nodes: number
  structuralEdges: number
  proposedLoaded: number
  proposedTotal: number
}

export interface UseForceSimulationResult {
  svgRef: React.RefObject<SVGSVGElement | null>
  tooltipState: TooltipState | null
  stats: GraphStats
  setSearchQuery: (q: string) => void
  setShowEdges: (show: boolean) => void
  setShowProposed: (show: boolean) => void
  setPinnedNodeId: (id: string | null) => void
  clearHover: () => void
}

function linkId(edge: SimLink): string {
  const s = typeof edge.source === 'object' && edge.source ? (edge.source as SimNode).id : String(edge.source)
  const t = typeof edge.target === 'object' && edge.target ? (edge.target as SimNode).id : String(edge.target)
  return `${s}|${t}|${edge.edge_type}`
}

function buildAdjacency(edges: GraphEdge[]): Map<string, GraphEdge[]> {
  const adj = new Map<string, GraphEdge[]>()
  for (const e of edges) {
    const u = e.source
    const v = e.target
    if (!adj.has(u)) adj.set(u, [])
    if (!adj.has(v)) adj.set(v, [])
    adj.get(u)!.push(e)
    adj.get(v)!.push(e)
  }
  return adj
}

const BATCH_SIZE = 500

export function useForceSimulation(data: GraphData): UseForceSimulationResult {
  const svgRef = useRef<SVGSVGElement | null>(null)
  const simRef = useRef<d3.Simulation<SimNode, SimLink> | null>(null)
  const zoomRef = useRef<d3.ZoomBehavior<SVGSVGElement, unknown> | null>(null)
  const gRef = useRef<d3.Selection<SVGGElement, unknown, null, undefined> | null>(null)
  const linkSelectionRef = useRef<d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null>(null)
  const nodeSelectionRef = useRef<d3.Selection<SVGCircleElement, SimNode, SVGGElement, unknown> | null>(null)
  const proposedSelectionRef = useRef<d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null>(null)
  const hoverRef = useRef<string | null>(null)
  const pinnedRef = useRef<string | null>(null)
  const searchRef = useRef('')
  const showEdgesRef = useRef(true)
  const showProposedRef = useRef(false)
  const injectedRef = useRef<SimLink[]>([])
  const proposedQueueRef = useRef<SimLink[]>([])
  const simLinksRef = useRef<SimLink[]>([])
  const nodesRef = useRef<SimNode[]>([])
  const adjRef = useRef<Map<string, GraphEdge[]>>(new Map())
  const rootIdRef = useRef<string | null>(null)
  const groupHuesRef = useRef<Map<string, number>>(new Map())
  const idleHandleRef = useRef<number | null>(null)
  const initializedRef = useRef(false)

  const [tooltipState, setTooltipState] = useState<TooltipState | null>(null)
  const [stats, setStats] = useState<GraphStats>({
    nodes: data.nodes.length,
    structuralEdges: data.structural_edges.length,
    proposedLoaded: 0,
    proposedTotal: data.proposed_edges.length,
  })

  const hideTooltip = useCallback(() => {
    setTooltipState(prev => (prev ? { ...prev, visible: false } : null))
  }, [])

  useEffect(() => {
    if (initializedRef.current) return
    initializedRef.current = true

    const rootId = findRootId(data.nodes)
    rootIdRef.current = rootId
    const groupHues = makeGroupHues(data.nodes)
    groupHuesRef.current = groupHues
    const adj = buildAdjacency([...data.structural_edges, ...data.proposed_edges])
    adjRef.current = adj

    const nodes = data.nodes.map(n => ({ ...n, x: 0, y: 0, vx: 0, vy: 0, fx: null, fy: null })) as unknown as SimNode[]
    nodesRef.current = nodes

    const groupNames = computeGroupNames(data.nodes)
    const angleStep = computeAngleStep(groupNames)
    const { centerX, centerY, orbitalRadius } = computeOrbitalParams(window.innerWidth, window.innerHeight)
    const groupCounters = new Map<string, number>()

    let keySeq = 0
    const tagKey = (e: SimLink) => { e._key = `${e.source}|${e.target}|${e.edge_type}|${keySeq++}`; return e }

    const simLinks = data.structural_edges.map(e => ({ ...e, source: e.source, target: e.target })) as SimLink[]
    simLinks.forEach(tagKey)
    simLinksRef.current = simLinks

    const proposedQueue = data.proposed_edges.map(e => ({ ...e, source: e.source, target: e.target })) as SimLink[]
    proposedQueue.forEach(tagKey)
    proposedQueueRef.current = proposedQueue

    injectedRef.current = []

    const svg = d3.select(svgRef.current!)
    svg.selectAll('*').remove()

    const g = svg.append('g')
    gRef.current = g

    const width = window.innerWidth
    const height = window.innerHeight
    svg.attr('width', width).attr('height', height)

    const zoom = d3.zoom<SVGSVGElement, unknown>().scaleExtent([0.05, 8]).on('zoom', (e) => {
      g.attr('transform', e.transform as string)
    })
    zoomRef.current = zoom
    svg.call(zoom)

    const linkLayer = g.append('g')
    const nodeLayer = g.append('g')

    function updateOpacities(hovered: string | null, pinned: string | null, query: string) {
      const activeId = hovered || pinned
      const sNodes = nodeSelectionRef.current
      const sLinks = linkSelectionRef.current
      if (!sNodes || !sLinks) return

      if (!activeId) {
        if (query) {
          sNodes.style('opacity', (n: SimNode) => (n.title || n.id).toLowerCase().includes(query) ? 1 : 0.08)
          sLinks.style('opacity', (l: SimLink) => linkId(l).toLowerCase().includes(query) ? 0.5 : 0)
        } else {
          sNodes.style('opacity', null).attr('stroke-width', 1.5)
          sLinks.style('opacity', null)
        }
        return
      }

      const neighbors = new Set<string>()
      const adjList = adjRef.current.get(activeId) || []
      for (const e of adjList) {
        neighbors.add(e.source === activeId ? e.target : e.source)
      }
      neighbors.add(activeId)

      sNodes
        .style('opacity', (n: SimNode) => neighbors.has(n.id) ? 1 : 0.08)
        .attr('stroke-width', (n: SimNode) => n.id === activeId ? 4 : 1.5)
        .attr('stroke', (n: SimNode) => n.id === activeId ? '#fff' : 'none')

      sLinks.style('opacity', (l: SimLink) => {
        const sid = typeof l.source === 'object' ? (l.source as SimNode).id : String(l.source)
        const tid = typeof l.target === 'object' ? (l.target as SimNode).id : String(l.target)
        return sid === activeId || tid === activeId ? 0.7 : 0.04
      })
    }

    function showTooltipNode(d: SimNode) {
      const t = d3.zoomTransform(svg.node()!)
      const connected = adjRef.current.get(d.id) || []
      setTooltipState({
        type: 'node',
        data: d,
        connectedEdges: connected,
        x: t.applyX(d.x) + 14,
        y: t.applyY(d.y) - 10,
        visible: true,
      })
    }

    function showTooltipEdge(l: SimLink) {
      const t = d3.zoomTransform(svg.node()!)
      const sx = typeof l.source === 'object' ? (l.source as SimNode).x : 0
      const sy = typeof l.source === 'object' ? (l.source as SimNode).y : 0
      const tx = typeof l.target === 'object' ? (l.target as SimNode).x : 0
      const ty = typeof l.target === 'object' ? (l.target as SimNode).y : 0
      const src = typeof l.source === 'object' ? (l.source as SimNode).id : String(l.source)
      const tgt = typeof l.target === 'object' ? (l.target as SimNode).id : String(l.target)
      setTooltipState({
        type: 'edge',
        data: { source: src, target: tgt, edge_type: l.edge_type, confidence: l.confidence },
        connectedEdges: [],
        x: t.applyX((sx + tx) / 2) + 14,
        y: t.applyY((sy + ty) / 2) - 10,
        visible: true,
      })
    }

    const linkSel = linkLayer
      .selectAll<SVGLineElement, SimLink>('.link')
      .data(simLinks, (d: SimLink) => d._key!)
      .join('line')
      .attr('class', 'link')
      .attr('stroke', d => d.edge_type === 'contains' ? '#7ec8e3' : '#888')
      .attr('stroke-width', d => d.edge_type === 'contains' ? 1.4 : 0.8)
      .attr('opacity', d => d.edge_type === 'contains' ? 0.8 : 0.6)
    linkSelectionRef.current = linkSel as unknown as typeof linkSelectionRef.current

    const nodeSel = nodeLayer
      .selectAll<SVGCircleElement, SimNode>('.node')
      .data(nodes, (d: SimNode) => d.id)
      .join('circle')
      .attr('class', 'node')
      .attr('r', (d: SimNode) => nodeR(d, rootId))
      .attr('fill', (d: SimNode) => nodeColor(d, rootId, groupHues))
    nodeSelectionRef.current = nodeSel as unknown as typeof nodeSelectionRef.current

    const sim = d3.forceSimulation(nodes)
      .force('link', d3.forceLink<SimNode, SimLink>(simLinks).id(d => d.id).distance(50).strength(d => d.edge_type === 'contains' ? 0.5 : 0.1))
      .force('charge', d3.forceManyBody<SimNode>().strength(-260).distanceMax(400))
      .force('x', d3.forceX<SimNode>(d => computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, groupCounters).x).strength(d => d.id === rootId ? 0.6 : 0.06))
      .force('y', d3.forceY<SimNode>(d => computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, groupCounters).y).strength(d => d.id === rootId ? 0.6 : 0.06))
      .force('collide', d3.forceCollide<SimNode>(d => nodeR(d, rootId) + 6).strength(1))
      .alphaDecay(0.015)
      .alphaMin(0.01)
    simRef.current = sim

    sim.on('tick', () => {
      linkSel
        .attr('x1', d => (d.source as SimNode).x)
        .attr('y1', d => (d.source as SimNode).y)
        .attr('x2', d => (d.target as SimNode).x)
        .attr('y2', d => (d.target as SimNode).y)
      nodeSel.attr('cx', d => d.x).attr('cy', d => d.y)

      const pSel = proposedSelectionRef.current
      if (pSel) {
        pSel
          .attr('x1', d => {
            const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
            const n = nodesRef.current.find(n => n.id === sid)
            return n ? n.x : 0
          })
          .attr('y1', d => {
            const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
            const n = nodesRef.current.find(n => n.id === sid)
            return n ? n.y : 0
          })
          .attr('x2', d => {
            const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
            const n = nodesRef.current.find(n => n.id === tid)
            return n ? n.x : 0
          })
          .attr('y2', d => {
            const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
            const n = nodesRef.current.find(n => n.id === tid)
            return n ? n.y : 0
          })
      }
    })

    nodeSel
      .on('mouseenter', function (_event: MouseEvent, d: SimNode) {
        if (searchRef.current && !(d.title || d.id).toLowerCase().includes(searchRef.current)) return
        hoverRef.current = d.id
        updateOpacities(d.id, pinnedRef.current, searchRef.current)
        showTooltipNode(d)
      })
      .on('mouseleave', function () {
        if (pinnedRef.current && pinnedRef.current === hoverRef.current) return
        hoverRef.current = null
        updateOpacities(hoverRef.current, pinnedRef.current, searchRef.current)
        hideTooltip()
      })

    linkSel
      .on('mouseenter', function (_event: MouseEvent, d: SimLink) {
        showTooltipEdge(d)
      })
      .on('mouseleave', function () {
        if (!pinnedRef.current) hideTooltip()
      })

    svg.on('click', (event: MouseEvent) => {
      if (event.target === svg.node()) {
        pinnedRef.current = null
        hoverRef.current = null
        updateOpacities(null, null, searchRef.current)
        hideTooltip()
      }
    })

    nodeSel.on('click', function (event: MouseEvent, d: SimNode) {
      event.stopPropagation()
      pinnedRef.current = pinnedRef.current === d.id ? null : d.id
      hoverRef.current = pinnedRef.current
      updateOpacities(hoverRef.current, pinnedRef.current, searchRef.current)
      if (pinnedRef.current) showTooltipNode(d)
      else hideTooltip()
    })

    const proposedNodeIndex = new Map(nodes.map(n => [n.id, n]))
    function drawProposedTick() {
      const pSel = proposedSelectionRef.current
      if (!pSel) return
      pSel
        .attr('x1', d => {
          const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
          const n = proposedNodeIndex.get(sid)
          return n ? n.x : 0
        })
        .attr('y1', d => {
          const sid = typeof d.source === 'string' ? d.source : (d.source as SimNode).id
          const n = proposedNodeIndex.get(sid)
          return n ? n.y : 0
        })
        .attr('x2', d => {
          const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
          const n = proposedNodeIndex.get(tid)
          return n ? n.x : 0
        })
        .attr('y2', d => {
          const tid = typeof d.target === 'string' ? d.target : (d.target as SimNode).id
          const n = proposedNodeIndex.get(tid)
          return n ? n.y : 0
        })
    }

    function idleBatch(deadline: IdleDeadline | null) {
      let processed = 0
      const q = proposedQueueRef.current
      while (q.length > 0 && processed < BATCH_SIZE && (deadline ? deadline.timeRemaining() > 1 : true)) {
        injectedRef.current.push(q.shift()!)
        processed++
      }
      if (processed > 0) {
        const pSel = linkLayer
          .selectAll<SVGLineElement, SimLink>('.proposed-link')
          .data(injectedRef.current, (d: SimLink) => d._key!)
          .join('line')
          .attr('class', 'proposed-link')
          .attr('stroke', '#e8a33d')
          .attr('stroke-width', 0.3)
          .style('display', (showProposedRef.current ? null : 'none') as any)
          .attr('opacity', 0.08)
        proposedSelectionRef.current = pSel as unknown as typeof proposedSelectionRef.current
        drawProposedTick()

        setStats({
          nodes: data.nodes.length,
          structuralEdges: data.structural_edges.length,
          proposedLoaded: injectedRef.current.length,
          proposedTotal: injectedRef.current.length + q.length,
        })
      }
      if (q.length > 0) scheduleIdle()
    }

    function scheduleIdle() {
      if (typeof (window as any).requestIdleCallback !== 'undefined') {
        idleHandleRef.current = (window as any).requestIdleCallback(idleBatch, { timeout: 1000 }) as number
      } else {
        idleHandleRef.current = window.setTimeout(() => idleBatch(null), 100) as unknown as number
      }
    }

    const timeoutId = window.setTimeout(scheduleIdle, 600)

    return () => {
      sim.stop()
      svg.on('.', null)
      svg.selectAll('*').remove()
      if (idleHandleRef.current !== null) {
        if (typeof (window as any).requestIdleCallback !== 'undefined') {
          ;(window as any).cancelIdleCallback(idleHandleRef.current)
        } else {
          window.clearTimeout(idleHandleRef.current)
        }
      }
      window.clearTimeout(timeoutId)
      initializedRef.current = false
    }
  }, [data])

  const setSearchQuery = useCallback((q: string) => {
    searchRef.current = q.toLowerCase()
    const nodeSel = nodeSelectionRef.current
    const linkSel = linkSelectionRef.current
    if (!nodeSel || !linkSel) return
    const h = hoverRef.current
    const p = pinnedRef.current
    const activeId = h || p

    if (!activeId && !q) {
      nodeSel.style('opacity', null).attr('stroke-width', 1.5).attr('stroke', 'none')
      linkSel.style('opacity', null)
      return
    }

    if (activeId) {
      const neighbors = new Set<string>()
      const adjList = adjRef.current.get(activeId) || []
      for (const e of adjList) {
        neighbors.add(e.source === activeId ? e.target : e.source)
      }
      neighbors.add(activeId)
      nodeSel
        .style('opacity', (n: SimNode) => neighbors.has(n.id) ? 1 : 0.08)
        .attr('stroke-width', (n: SimNode) => n.id === activeId ? 4 : 1.5)
        .attr('stroke', (n: SimNode) => n.id === activeId ? '#fff' : 'none')
      linkSel.style('opacity', (l: SimLink) => {
        const sid = typeof l.source === 'object' ? (l.source as SimNode).id : String(l.source)
        const tid = typeof l.target === 'object' ? (l.target as SimNode).id : String(l.target)
        return sid === activeId || tid === activeId ? 0.7 : 0.04
      })
    } else {
      nodeSel.style('opacity', (n: SimNode) => (n.title || n.id).toLowerCase().includes(q) ? 1 : 0.08)
      linkSel.style('opacity', (l: SimLink) => linkId(l).toLowerCase().includes(q) ? 0.5 : 0)
    }
  }, [])

  const setShowEdgesFn = useCallback((show: boolean) => {
    showEdgesRef.current = show
    if (show) {
      linkSelectionRef.current?.style('display', null as any)
    } else {
      linkSelectionRef.current?.style('display', 'none')
    }
    if (!show) {
      proposedSelectionRef.current?.style('display', 'none')
    } else if (showProposedRef.current) {
      proposedSelectionRef.current?.style('display', null as any)
    }
  }, [])

  const setShowProposedFn = useCallback((show: boolean) => {
    showProposedRef.current = show
    if (!showEdgesRef.current) {
      proposedSelectionRef.current?.style('display', 'none')
    } else if (show) {
      proposedSelectionRef.current?.style('display', null as any)
    } else {
      proposedSelectionRef.current?.style('display', 'none')
    }
  }, [])

  const setPinnedNodeId = useCallback((id: string | null) => {
    pinnedRef.current = id
    const nodeSel = nodeSelectionRef.current
    const linkSel = linkSelectionRef.current
    if (!nodeSel || !linkSel) return
    const h = id
    const activeId = h

    if (!activeId) {
      nodeSel.style('opacity', null).attr('stroke-width', 1.5).attr('stroke', 'none')
      linkSel.style('opacity', null)
      hideTooltip()
      return
    }

    const neighbors = new Set<string>()
    const adjList = adjRef.current.get(activeId) || []
    for (const e of adjList) {
      neighbors.add(e.source === activeId ? e.target : e.source)
    }
    neighbors.add(activeId)
    nodeSel
      .style('opacity', (n: SimNode) => neighbors.has(n.id) ? 1 : 0.08)
      .attr('stroke-width', (n: SimNode) => n.id === activeId ? 4 : 1.5)
      .attr('stroke', (n: SimNode) => n.id === activeId ? '#fff' : 'none')
    linkSel.style('opacity', (l: SimLink) => {
      const sid = typeof l.source === 'object' ? (l.source as SimNode).id : String(l.source)
      const tid = typeof l.target === 'object' ? (l.target as SimNode).id : String(l.target)
      return sid === activeId || tid === activeId ? 0.7 : 0.04
    })

    const node = nodesRef.current.find(n => n.id === id)
    if (node) {
      const t = d3.zoomTransform(svgRef.current!)
      const connected = adjRef.current.get(id) || []
      setTooltipState({
        type: 'node',
        data: node,
        connectedEdges: connected,
        x: t.applyX(node.x) + 14,
        y: t.applyY(node.y) - 10,
        visible: true,
      })
    }
  }, [])

  const clearHover = useCallback(() => {
    hoverRef.current = null
    if (!pinnedRef.current) {
      const nodeSel = nodeSelectionRef.current
      const linkSel = linkSelectionRef.current
      if (nodeSel && linkSel) {
        nodeSel.style('opacity', null).attr('stroke-width', 1.5).attr('stroke', 'none')
        linkSel.style('opacity', null)
      }
      hideTooltip()
    }
  }, [])

  return {
    svgRef,
    tooltipState,
    stats,
    setSearchQuery,
    setShowEdges: setShowEdgesFn,
    setShowProposed: setShowProposedFn,
    setPinnedNodeId,
    clearHover,
  }
}
