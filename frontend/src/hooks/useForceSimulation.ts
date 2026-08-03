import { useRef, useState, useEffect, useCallback, useMemo } from 'react'
import * as d3 from 'd3'
import type { GraphData, SimNode, SimLink, GraphNode, GraphEdge } from '../types/graph'
import { nodeColor } from '../utils/colors'
import { useD3Simulation } from './useD3Simulation'
import type { GraphInteractionResult } from './useGraphInteraction'
import { useGraphInteraction } from './useGraphInteraction'
import type { ProposedBatchResult } from './useProposedBatch'
import { useProposedBatch } from './useProposedBatch'

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
  handleResize: (width: number, height: number) => void
  setSearchQuery: (q: string) => void
  setShowEdges: (show: boolean) => void
  setShowProposed: (show: boolean) => void
  setEdgeOpacity: (v: number) => void
  setHueOffset: (v: number) => void
  setDensity: (v: number) => void
  setPinnedNodeId: (id: string | null) => void
  clearHover: () => void
}

export const SIM_DEFAULTS = {
  showEdges: true,
  showProposed: false,
  edgeOpacity: 0.6,
  hueOffset: 0,
  density: 50,
}

function buildAdjacency(edges: GraphEdge[]): Map<string, GraphEdge[]> {
  const adj = new Map<string, GraphEdge[]>()
  for (const e of edges) {
    if (!adj.has(e.source)) adj.set(e.source, [])
    if (!adj.has(e.target)) adj.set(e.target, [])
    adj.get(e.source)!.push(e)
    adj.get(e.target)!.push(e)
  }
  return adj
}

export function useForceSimulation(data: GraphData): UseForceSimulationResult {
  const svgRef = useRef<SVGSVGElement | null>(null)
  const nodeSelectionRef = useRef<d3.Selection<SVGCircleElement, SimNode, SVGGElement, unknown> | null>(null)
  const linkSelectionRef = useRef<d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null>(null)
  const proposedSelectionRef = useRef<d3.Selection<SVGLineElement, SimLink, SVGGElement, unknown> | null>(null)
  const gRef = useRef<d3.Selection<SVGGElement, unknown, null, undefined> | null>(null)
  const linkLayerRef = useRef<d3.Selection<SVGGElement, unknown, null, undefined> | null>(null)
  const showEdgesRef = useRef<boolean>(SIM_DEFAULTS.showEdges)
  const showProposedRef = useRef<boolean>(SIM_DEFAULTS.showProposed)
  const edgeOpacityRef = useRef<number>(SIM_DEFAULTS.edgeOpacity)
  const hueOffsetRef = useRef<number>(SIM_DEFAULTS.hueOffset)
  const densityRef = useRef<number>(SIM_DEFAULTS.density)

  const adjacency = useMemo(
    () => buildAdjacency([...data.structural_edges, ...data.proposed_edges]),
    [data.structural_edges, data.proposed_edges],
  )

  const d3sim = useD3Simulation(data.nodes, data.structural_edges)
  const interact = useGraphInteraction(adjacency, d3sim.nodesRef)
  const batch = useProposedBatch(data.proposed_edges)

  const interactRef = useRef<GraphInteractionResult>(interact)
  interactRef.current = interact

  const batchRef = useRef<ProposedBatchResult>(batch)
  batchRef.current = batch

  const d3simRef = useRef(d3sim)
  d3simRef.current = d3sim

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

  const applyOpacity = useCallback(() => {
    const i = interactRef.current
    const nodeSel = nodeSelectionRef.current
    const linkSel = linkSelectionRef.current
    if (!nodeSel || !linkSel) return

    if (!i.isInteracting()) {
      nodeSel.style('opacity', null).attr('stroke-width', 1.5).attr('stroke', 'none')
      linkSel.style('opacity', null)
      return
    }

    nodeSel
      .style('opacity', (d: SimNode) => i.nodeOpacityRule(d.id))
      .attr('stroke-width', (d: SimNode) => i.nodeStrokeRule(d.id).width)
      .attr('stroke', (d: SimNode) => i.nodeStrokeRule(d.id).color)

    linkSel.style('opacity', (l: SimLink) => {
      const sid = typeof l.source === 'object' ? (l.source as SimNode).id : String(l.source)
      const tid = typeof l.target === 'object' ? (l.target as SimNode).id : String(l.target)
      return i.linkOpacityRule(sid, tid)
    })
  }, [])

  useEffect(() => {
    if (data.nodes.length === 0) return

    const { simRef: { current: sim }, nodesRef, simLinksRef, rootId, groupHues } = d3simRef.current
    const { handleNodeEnter, handleNodeLeave, handleNodeClick, clearAll, getPinnedId } = interactRef.current
    if (!sim) return

    const showTooltipNode = (d: SimNode) => {
      const t = d3.zoomTransform(svgRef.current!)
      const connected = adjacency.get(d.id) || []
      setTooltipState({
        type: 'node',
        data: d,
        connectedEdges: connected,
        x: t.applyX(d.x) + 14,
        y: t.applyY(d.y) - 10,
        visible: true,
      })
    }

    const showTooltipEdge = (l: SimLink) => {
      const t = d3.zoomTransform(svgRef.current!)
      const sx = (l.source as SimNode).x
      const sy = (l.source as SimNode).y
      const tx = (l.target as SimNode).x
      const ty = (l.target as SimNode).y
      const src = (l.source as SimNode).id
      const tgt = (l.target as SimNode).id
      setTooltipState({
        type: 'edge',
        data: { source: src, target: tgt, edge_type: l.edge_type, confidence: l.confidence },
        connectedEdges: [],
        x: t.applyX((sx + tx) / 2) + 14,
        y: t.applyY((sy + ty) / 2) - 10,
        visible: true,
      })
    }

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
    svg.call(zoom)

    const linkLayer = g.append('g')
    linkLayerRef.current = linkLayer
    const nodeLayer = g.append('g')

    const linkSel = linkLayer
      .selectAll<SVGLineElement, SimLink>('.link')
      .data(simLinksRef.current, (d: SimLink) => d._key!)
      .join('line')
      .attr('class', 'link')
      .attr('stroke', d => d._crossProject ? '#a78bfa' : '#aaa')
      .attr('stroke-width', d => d._crossProject ? 2 : 1)
      .attr('opacity', d => d._crossProject ? 1 : edgeOpacityRef.current)
    linkSelectionRef.current = linkSel

    const nodeSel = nodeLayer
      .selectAll<SVGCircleElement, SimNode>('.node')
      .data(nodesRef.current, (d: SimNode) => d.id)
      .join('circle')
      .attr('class', 'node')
      .attr('r', d => d.id === rootId ? 18 : d.type === 'directory' ? 5
        : Math.min(14, Math.max(3, Math.log10(d.size || 1) * 3 + 2)))
      .attr('fill', d => nodeColor(d, rootId, groupHues, 0))
    nodeSelectionRef.current = nodeSel

    const drag = d3.drag<SVGCircleElement, SimNode>()
      .on('start', function (event, d) {
        if (!event.active) sim.alphaTarget(0.3).restart()
        d.fx = d.x
        d.fy = d.y
      })
      .on('drag', function (event, d) {
        d.fx = event.x
        d.fy = event.y
      })
      .on('end', function (event, d) {
        if (!event.active) sim.alphaTarget(0)
        if (!getPinnedId()) {
          d.fx = null
          d.fy = null
        }
      })
    nodeSel.call(drag)

    nodeSel
      .on('mouseenter', function (_event, d) {
        handleNodeEnter(d.id)
        applyOpacity()
        showTooltipNode(d)
      })
      .on('mouseleave', function () {
        handleNodeLeave()
        applyOpacity()
        if (!getPinnedId()) hideTooltip()
      })
      .on('click', function (event, d) {
        event.stopPropagation()
        handleNodeClick(d.id)
        applyOpacity()
        if (getPinnedId()) showTooltipNode(d)
        else hideTooltip()
      })

    linkSel
      .on('mouseenter', function (_event, d) {
        showTooltipEdge(d)
      })
      .on('mouseleave', function () {
        if (!getPinnedId()) hideTooltip()
      })

    svg.on('click', (event) => {
      if (event.target === svg.node()) {
        clearAll()
        applyOpacity()
        hideTooltip()
      }
    })

    const nodeIndex = new Map<string, SimNode>()
    for (const n of nodesRef.current) {
      nodeIndex.set(n.id, n)
    }

    sim.on('tick', () => {
      linkSel
        .attr('x1', d => (d.source as SimNode).x)
        .attr('y1', d => (d.source as SimNode).y)
        .attr('x2', d => (d.target as SimNode).x)
        .attr('y2', d => (d.target as SimNode).y)
      nodeSel.attr('cx', d => d.x).attr('cy', d => d.y)
      proposedSelectionRef.current &&
        batchRef.current.updatePositions(proposedSelectionRef.current, nodeIndex)
    })

    sim.alpha(1).restart()
    sim.on('end', () => sim.stop())

    const afterInject = () => {
      const pSel = linkLayer
        .selectAll<SVGLineElement, SimLink>('.proposed-link')
        .data(batchRef.current.injectedEdgesRef.current, (d: SimLink) => d._key!)
        .join('line')
        .attr('class', 'proposed-link')
        .attr('stroke', '#e8a33d')
        .attr('stroke-width', 0.3)
        .attr('opacity', showProposedRef.current && showEdgesRef.current ? edgeOpacityRef.current * 0.15 : 0)
      proposedSelectionRef.current = pSel
      batchRef.current.updatePositions(pSel, nodeIndex)

      setStats({
        nodes: data.nodes.length,
        structuralEdges: data.structural_edges.length,
        proposedLoaded: batchRef.current.injectedEdgesRef.current.length,
        proposedTotal: data.proposed_edges.length,
      })
    }

    batchRef.current.scheduleBatch(linkLayerRef, afterInject)

    return () => {
      sim.stop()
      svg.on('.', null)
      svg.selectAll('*').remove()
      gRef.current = null
      linkSelectionRef.current = null
      nodeSelectionRef.current = null
      proposedSelectionRef.current = null
      linkLayerRef.current = null
    }
  }, [data.nodes, data.structural_edges, data.proposed_edges, adjacency, applyOpacity, hideTooltip])

  const setSearchQuery = useCallback((q: string) => {
    interactRef.current.setSearchQuery(q)
    applyOpacity()
  }, [applyOpacity])

  const setShowEdges = useCallback((show: boolean) => {
    showEdgesRef.current = show
    const ls = linkSelectionRef.current
    if (show) { ls?.style('display', null as unknown as string) }
    else { ls?.style('display', 'none') }
    const ps = proposedSelectionRef.current
    if (ps) {
      ps.attr('opacity', show && showProposedRef.current ? edgeOpacityRef.current * 0.15 : 0)
    }
  }, [])

  const setShowProposed = useCallback((show: boolean) => {
    showProposedRef.current = show
    const ps = proposedSelectionRef.current
    if (ps) {
      ps.attr('opacity', show && showEdgesRef.current ? edgeOpacityRef.current * 0.15 : 0)
    }
  }, [])

  const setEdgeOpacity = useCallback((v: number) => {
    edgeOpacityRef.current = v
    linkSelectionRef.current?.attr('opacity', v)
    const ps = proposedSelectionRef.current
    if (ps) ps.attr('opacity', showProposedRef.current && showEdgesRef.current ? v * 0.15 : 0)
  }, [])

  const setHueOffset = useCallback((v: number) => {
    hueOffsetRef.current = v
    const nodeSel = nodeSelectionRef.current
    if (!nodeSel) return
    const { rootId, groupHues } = d3simRef.current
    nodeSel.attr('fill', (d: SimNode) => nodeColor(d, rootId, groupHues, v))
  }, [])

  const setDensity = useCallback((v: number) => {
    densityRef.current = v
    const strength = -50 - (v / 100) * 950
    d3simRef.current.setChargeStrength(strength)
  }, [])

  const setPinnedNodeId = useCallback((id: string | null) => {
    if (id) {
      interactRef.current.pinNode(id)
    } else {
      interactRef.current.clearAll()
    }
    applyOpacity()
  }, [applyOpacity])

  const clearHover = useCallback(() => {
    interactRef.current.clearHover()
    applyOpacity()
  }, [applyOpacity])

  const handleResize = useCallback((width: number, height: number) => {
    d3simRef.current.handleResize(width, height)
  }, [])

  return {
    svgRef,
    tooltipState,
    stats,
    handleResize,
    setSearchQuery,
    setShowEdges,
    setShowProposed,
    setEdgeOpacity,
    setHueOffset,
    setDensity,
    setPinnedNodeId,
    clearHover,
  }
}
