import { useGraphData } from './hooks/useGraphData'
import { useForceSimulation } from './hooks/useForceSimulation'
import { GraphCanvas } from './components/GraphCanvas'
import { Controls } from './components/Controls'
import { Tooltip } from './components/Tooltip'
import { Stats } from './components/Stats'
import type { GraphData } from './types/graph'

export function App() {
  const { data, loading, error } = useGraphData()

  if (loading) return <div className="app-status">Loading graph...</div>
  if (error) return <div className="app-status">Error: {error}</div>
  if (!data) return <div className="app-status">No data</div>

  return <GraphView data={data} />
}

function GraphView({ data }: { data: GraphData }) {
  const {
    svgRef,
    tooltipState,
    stats,
    setSearchQuery,
    setShowEdges,
    setShowProposed,
    setEdgeOpacity,
    setHueOffset,
    setDensity,
    setPinnedNodeId,
  } = useForceSimulation(data)

  return (
    <>
      <GraphCanvas svgRef={svgRef} />
      <Controls
        onSearch={setSearchQuery}
        onEdgesToggle={setShowEdges}
        onProposedToggle={setShowProposed}
        onEdgeOpacity={setEdgeOpacity}
        onHueOffset={setHueOffset}
        onDensity={setDensity}
      />
      <Tooltip state={tooltipState} onPin={setPinnedNodeId} />
      <Stats stats={stats} />
    </>
  )
}
