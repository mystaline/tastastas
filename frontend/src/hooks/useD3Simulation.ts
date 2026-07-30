import { useRef, useEffect } from 'react'
import * as d3 from 'd3'
import type { GraphNode, GraphEdge, SimNode, SimLink } from '../types/graph'
import { findRootId, makeGroupHues, nodeR } from '../utils/colors'
import { computeGroupNames, computeAngleStep, computeOrbitalParams, computeOrbitalTarget } from '../utils/layout'

export interface D3SimResult {
  simRef: React.MutableRefObject<d3.Simulation<SimNode, SimLink> | null>
  nodesRef: React.MutableRefObject<SimNode[]>
  linksRef: React.MutableRefObject<SimLink[]>
  simLinksRef: React.MutableRefObject<SimLink[]>
  rootId: string | null
  groupHues: Map<string, number>
  restart: (alpha?: number) => void
  setChargeStrength: (strength: number) => void
  handleResize: (width: number, height: number) => void
}

export function useD3Simulation(
  nodes: GraphNode[],
  structuralEdges: GraphEdge[],
): D3SimResult {
  const simRef = useRef<d3.Simulation<SimNode, SimLink> | null>(null)
  const nodesRef = useRef<SimNode[]>([])
  const linksRef = useRef<SimLink[]>([])
  const simLinksRef = useRef<SimLink[]>([])
  const groupCountersRef = useRef<Map<string, number>>(new Map())

  const rootId = findRootId(nodes)
  const groupHues = makeGroupHues(nodes)
  const groupNames = computeGroupNames(nodes)
  const angleStep = computeAngleStep(groupNames)

  const simNodes: SimNode[] = nodes.map(n => ({ ...n, x: 0, y: 0, vx: 0, vy: 0, fx: null, fy: null }))
  nodesRef.current = simNodes

  let keySeq = 0
  const simLinks: SimLink[] = structuralEdges.map(e => ({
    ...e,
    source: e.source,
    target: e.target,
    _key: `${e.source}|${e.target}|${e.edge_type}|${keySeq++}`,
  }))
  simLinksRef.current = simLinks
  linksRef.current = simLinks

  const restart = (alpha = 0.3) => {
    const sim = simRef.current
    if (sim) { sim.alpha(alpha).restart() }
  }

  const setChargeStrength = (strength: number) => {
    const sim = simRef.current
    if (!sim) return
    sim.force('charge', d3.forceManyBody<SimNode>().strength(strength).distanceMax(650))
    restart()
  }

  const handleResize = (width: number, height: number) => {
    const sim = simRef.current
    if (!sim) return
    const { centerX, centerY, orbitalRadius } = computeOrbitalParams(width, height)
    const counters = new Map<string, number>()
    sim.force('x', d3.forceX<SimNode>(d =>
      computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters).x
    ).strength(d => d.id === rootId ? 0.6 : 0.04))
    sim.force('y', d3.forceY<SimNode>(d =>
      computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters).y
    ).strength(d => d.id === rootId ? 0.6 : 0.04))
    restart()
  }

  useEffect(() => {
    const { centerX, centerY, orbitalRadius } = computeOrbitalParams(window.innerWidth, window.innerHeight)
    const counters = groupCountersRef.current

    const sim = d3.forceSimulation(simNodes)
      .force('link', d3.forceLink<SimNode, SimLink>(simLinks).id(d => d.id).distance(90)
        .strength(d => d.edge_type === 'contains' ? 0.3 : 0.08))
      .force('charge', d3.forceManyBody<SimNode>().strength(-400).distanceMax(650))
      .force('x', d3.forceX<SimNode>(d =>
        computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters).x
      ).strength(d => d.id === rootId ? 0.6 : 0.04))
      .force('y', d3.forceY<SimNode>(d =>
        computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters).y
      ).strength(d => d.id === rootId ? 0.6 : 0.04))
      .force('collide', d3.forceCollide<SimNode>(d => nodeR(d, rootId) + 10).strength(1))
      .alphaDecay(0.015)
      .alphaMin(0.01)

    simRef.current = sim

    return () => {
      sim.stop()
      simRef.current = null
    }
  }, [])

  return {
    simRef,
    nodesRef,
    linksRef,
    simLinksRef,
    rootId,
    groupHues,
    restart,
    setChargeStrength,
    handleResize,
  }
}
