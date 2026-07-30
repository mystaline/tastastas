import type { GraphStats } from '../hooks/useForceSimulation'

export function Stats({ stats }: { stats: GraphStats }) {
  return (
    <div className="fixed bottom-4 left-4 text-xs text-emerald-400/70 z-10 font-mono">
      nodes: {stats.nodes} | structural: {stats.structuralEdges} | proposed: {stats.proposedLoaded}/{stats.proposedTotal}
    </div>
  )
}
