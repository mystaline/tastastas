import type { SimNode } from '../types/graph'

export function nodeMatch(node: SimNode, query: string): boolean {
  return (
    node.title.toLowerCase().includes(query) ||
    node.id.toLowerCase().includes(query) ||
    node.project_id.toLowerCase().includes(query) ||
    node.type.toLowerCase().includes(query)
  )
}
