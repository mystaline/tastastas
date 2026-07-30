import type { GraphStats } from '../hooks/useForceSimulation'

export function Stats({ stats }: { stats: GraphStats }) {
  return (
    <div
      style={{
        position: 'absolute',
        bottom: 10,
        left: 10,
        fontSize: 11,
        color: '#7a8',
        zIndex: 10,
        pointerEvents: 'auto',
      }}
    >
      nodes: {stats.nodes} | structural: {stats.structuralEdges} | proposed: {stats.proposedLoaded}/{stats.proposedTotal}
    </div>
  )
}
