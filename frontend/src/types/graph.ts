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
