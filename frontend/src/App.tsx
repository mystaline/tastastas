import { useEffect } from 'react'
import { useGraphData } from './hooks/useGraphData'
import { useForceSimulation, SIM_DEFAULTS } from './hooks/useForceSimulation'
import { GraphCanvas } from './components/GraphCanvas'
import { Controls } from './components/Controls'
import { Tooltip } from './components/Tooltip'
import { Stats } from './components/Stats'
import { ErrorBoundary } from './components/ErrorBoundary'
import { LoadingSkeleton } from './components/LoadingSkeleton'
import type { GraphData } from './types/graph'

function scopeLabel(projectID: string): string {
  const stage = new URLSearchParams(window.location.search).get('stage')
  return stage ? `${projectID} / ${stage}` : projectID
}

export function App() {
  const { loading, error, data, linkedProjects, selectedSidecars, toggleSidecar } = useGraphData()

  useEffect(() => {
    if (data) document.title = `Tastastas Graph — ${scopeLabel(data.project_id)}`
  }, [data])

  return (
    <ErrorBoundary>
      {loading && <LoadingSkeleton />}
      {error && (
        <div className="flex items-center justify-center h-screen bg-slate-950 text-red-400">
          Error: {error}
        </div>
      )}
      {data && (
        <GraphView
          data={data}
          linkedProjects={linkedProjects}
          selectedSidecars={selectedSidecars}
          onToggleSidecar={toggleSidecar}
        />
      )}
    </ErrorBoundary>
  )
}

interface GraphViewProps {
  data: GraphData
  linkedProjects: string[]
  selectedSidecars: string[]
  onToggleSidecar: (p: string) => void
}

function GraphView({ data, linkedProjects, selectedSidecars, onToggleSidecar }: GraphViewProps) {
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
        linkedProjects={linkedProjects}
        selectedSidecars={selectedSidecars}
        onToggleSidecar={onToggleSidecar}
      />
      <Tooltip state={sim.tooltipState} onPin={sim.setPinnedNodeId} />
      <Stats stats={sim.stats} />
    </>
  )
}
