import { useGraphData } from './hooks/useGraphData'
import { useForceSimulation, SIM_DEFAULTS } from './hooks/useForceSimulation'
import { GraphCanvas } from './components/GraphCanvas'
import { Controls } from './components/Controls'
import { Tooltip } from './components/Tooltip'
import { Stats } from './components/Stats'
import { ErrorBoundary } from './components/ErrorBoundary'
import { LoadingSkeleton } from './components/LoadingSkeleton'
import type { GraphData } from './types/graph'

export function App() {
  const { loading, error, data } = useGraphData()

  return (
    <ErrorBoundary>
      {loading && <LoadingSkeleton />}
      {error && (
        <div className="flex items-center justify-center h-screen bg-slate-950 text-red-400">
          Error: {error}
        </div>
      )}
      {data && <GraphView data={data} />}
    </ErrorBoundary>
  )
}

function GraphView({ data }: { data: GraphData }) {
  const sim = useForceSimulation(data)

  return (
    <>
      <GraphCanvas
        svgRef={sim.svgRef}
        onResize={sim.handleResize}
      />
      <Controls
        onSearch={sim.setSearchQuery}
        onEdgesToggle={sim.setShowEdges}
        onProposedToggle={sim.setShowProposed}
        onEdgeOpacity={sim.setEdgeOpacity}
        onHueOffset={sim.setHueOffset}
        onDensity={sim.setDensity}
        initialShowEdges={SIM_DEFAULTS.showEdges}
        initialEdgeOpacity={SIM_DEFAULTS.edgeOpacity}
        initialHueOffset={SIM_DEFAULTS.hueOffset}
        initialDensity={SIM_DEFAULTS.density}
      />
      <Tooltip state={sim.tooltipState} onPin={sim.setPinnedNodeId} />
      <Stats stats={sim.stats} />
    </>
  )
}
