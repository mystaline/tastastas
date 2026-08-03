import { useRef, useEffect, useMemo } from 'react'
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

const CROSS_PROJECT_EDGE_TYPES = new Set(['cross-project-call', 'depends_on'])

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
  const groupHues = useMemo(() => makeGroupHues(nodes), [nodes])
  const groupNames = useMemo(() => computeGroupNames(nodes), [nodes])
  const primaryProjectId = nodes.find(n => n.id === rootId)?.project_id ?? nodes[0]?.project_id ?? ''
  const satelliteProjectIds = useMemo(
    () => [...new Set(nodes.map(n => n.project_id).filter(id => id !== primaryProjectId))],
    [nodes, primaryProjectId],
  )
  const angleStep = useMemo(() => computeAngleStep(groupNames), [groupNames])

  const simNodes: SimNode[] = useMemo(
    // Seed with a small deterministic spread, not a literal (0,0) stack —
    // forceManyBody has no defined repulsion direction for coincident
    // points, so a full-energy restart (alpha 1) blows a perfect stack
    // outward without bound instead of settling into clusters.
    () => nodes.map((n, i) => ({
      ...n,
      x: Math.cos(i) * 10,
      y: Math.sin(i) * 10,
      vx: 0, vy: 0, fx: null, fy: null,
    })),
    [nodes],
  )
  nodesRef.current = simNodes

  const simLinks: SimLink[] = useMemo(() => {
    let keySeq = 0
    return structuralEdges.map(e => ({
      ...e,
      source: e.source,
      target: e.target,
      _key: `${e.source}|${e.target}|${e.edge_type}|${keySeq++}`,
      _crossProject: CROSS_PROJECT_EDGE_TYPES.has(e.edge_type),
    }))
  }, [structuralEdges])
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
      computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters, satelliteProjectIds).x
    ).strength(d => d.id === rootId ? 0.6 : d.project_id === primaryProjectId ? 0.04 : 0.18))
    sim.force('y', d3.forceY<SimNode>(d =>
      computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters, satelliteProjectIds).y
    ).strength(d => d.id === rootId ? 0.6 : d.project_id === primaryProjectId ? 0.04 : 0.18))
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
        computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters, satelliteProjectIds).x
      ).strength(d => d.id === rootId ? 0.6 : d.project_id === primaryProjectId ? 0.04 : 0.18))
      .force('y', d3.forceY<SimNode>(d =>
        computeOrbitalTarget(d, rootId, groupNames, angleStep, centerX, centerY, orbitalRadius, counters, satelliteProjectIds).y
      ).strength(d => d.id === rootId ? 0.6 : d.project_id === primaryProjectId ? 0.04 : 0.18))
      .force('collide', d3.forceCollide<SimNode>(d => nodeR(d, rootId) + 10).strength(1))
      .alphaDecay(0.005)
      .alphaMin(0.005)

    simRef.current = sim

    return () => {
      sim.stop()
      simRef.current = null
    }
    // Rebuild the sim when the node/edge set changes so sidecar toggles and
    // any other data refresh re-sync the simulation; otherwise new nodes
    // default to (0,0) and never get a position.
  }, [simNodes, simLinks, rootId, groupNames, angleStep, primaryProjectId, satelliteProjectIds])

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
