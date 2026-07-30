import type * as d3 from 'd3'

export interface GraphNode {
  id: string
  title: string
  type: string
  group: string
  size: number
  weight: number
  project_id: string
}

export interface GraphEdge {
  source: string
  target: string
  edge_type: string
  confidence: number
}

export interface GraphData {
  project_id: string
  total_edges: number
  returned: number
  nodes: GraphNode[]
  structural_edges: GraphEdge[]
  proposed_edges: GraphEdge[]
}

export interface SimNode extends GraphNode {
  x: number
  y: number
  vx: number
  vy: number
  fx: number | null
  fy: number | null
}

export interface SimLink extends d3.SimulationLinkDatum<SimNode> {
  edge_type: string
  confidence: number
  _key?: string
}
