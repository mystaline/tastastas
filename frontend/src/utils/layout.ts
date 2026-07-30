import type { GraphNode } from '../types/graph'

export interface RadialTarget {
  x: number
  y: number
}

export function computeOrbitalTarget(
  d: GraphNode,
  rootId: string | null,
  groupNames: string[],
  angleStep: number,
  centerX: number,
  centerY: number,
  orbitalRadius: number,
  groupCounters: Map<string, number>,
): RadialTarget {
  if (d.id === rootId) return { x: centerX, y: centerY }
  const idx = groupNames.indexOf(d.group)
  const baseAngle = idx >= 0 ? idx * angleStep : 0
  const n = groupCounters.get(d.group) || 0
  groupCounters.set(d.group, n + 1)
  const jitterAngle = baseAngle + ((n % 40) - 20) * (angleStep / 80)
  const jitterRadius = orbitalRadius + Math.floor(n / 40) * 40
  return {
    x: centerX + Math.cos(jitterAngle) * jitterRadius,
    y: centerY + Math.sin(jitterAngle) * jitterRadius,
  }
}

export function computeGroupNames(nodes: GraphNode[]): string[] {
  return [...new Set(nodes.map(n => n.group))].filter(gr => gr !== 'dir')
}

export function computeOrbitalParams(width: number, height: number) {
  return {
    centerX: width / 2,
    centerY: height / 2,
    orbitalRadius: Math.min(width, height) * 0.3,
  }
}

export function computeAngleStep(groupNames: string[]): number {
  return (2 * Math.PI) / Math.max(groupNames.length, 1)
}
