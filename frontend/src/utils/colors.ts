import type { GraphNode } from '../types/graph'

const typeLightness: Record<string, number> = {
  'api-spec': 70,
  'erd': 60,
  'prd': 55,
  'prd-detail': 55,
  'architecture-decision': 65,
  'visual-design': 55,
  'design-doc': 52,
  'foundation-doc': 48,
  'test-case': 48,
  'code:function': 50,
  'code:type': 50,
  'code:method': 50,
  'convention': 48,
  'generic-doc': 48,
  'template': 48,
  'obsidian-note': 48,
  'directory': 45,
}

export function makeGroupHues(nodes: GraphNode[]): Map<string, number> {
  const hues = new Map<string, number>()
  nodes.forEach((n, i) => {
    if (!hues.has(n.group)) hues.set(n.group, (i * 137.5) % 360)
  })
  return hues
}

export function findRootId(nodes: GraphNode[]): string | null {
  const dirs = nodes.filter(n => n.type === 'directory' && n.id.endsWith('/'))
  return dirs.length ? dirs.reduce((a, b) => a.id.length <= b.id.length ? a : b).id : null
}

export function isRoot(d: GraphNode, rootId: string | null): boolean {
  return d.id === rootId
}

export function nodeR(d: GraphNode, rootId: string | null): number {
  if (isRoot(d, rootId)) return 18
  if (d.type === 'directory') return 5
  return Math.min(14, Math.max(3, Math.log10(d.size || 1) * 3 + 2))
}

export function nodeColor(
  d: GraphNode,
  rootId: string | null,
  groupHues: Map<string, number>,
): string {
  if (isRoot(d, rootId)) return 'hsl(45, 90%, 55%)'
  const hue = groupHues.get(d.group) ?? 220
  const lit = typeLightness[d.type] !== undefined ? typeLightness[d.type] : 48
  return `hsl(${hue}, 75%, ${lit}%)`
}

export function esc(s: string): string {
  const d = document.createElement('div')
  d.textContent = s
  return d.innerHTML
}
