import { useEffect } from 'react'
import { useGraphData } from './hooks/useGraphData'
import { useForceSimulation, SIM_DEFAULTS } from './hooks/useForceSimulation'
import { GraphCanvas } from './components/GraphCanvas'
import { Controls } from './components/Controls'
import { Tooltip } from './components/Tooltip'
import { Stats } from './components/Stats'
import { ErrorBoundary } from './components/ErrorBoundary'
import { LoadingSkeleton } from './components/LoadingSkeleton'
import { Dashboard } from './components/Dashboard'
import { getProjectFromPath } from './utils/route'
import type { GraphData } from './types/graph'

function scopeLabel(projectID: string): string {
  const stage = new URLSearchParams(window.location.search).get('stage')
  return stage ? `${projectID} / ${stage}` : projectID
}

export function App() {
  const project = getProjectFromPath()
  const { loading, error, data, linkedProjects, selectedSidecars, toggleSidecar } = useGraphData()

  useEffect(() => {
    if (data) document.title = `Tastastas Graph — ${scopeLabel(data.project_id)}`
  }, [data])

  return (
    <ErrorBoundary>
      {!project && <Dashboard />}
      {project && loading && <LoadingSkeleton />}
      {project && error && (
        <div className="flex items-center justify-center h-screen bg-slate-950 text-red-400">
          Error: {error}
        </div>
      )}
      {project && data && (
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
      <a
        href="/graph/"
        className="fixed top-3 left-3 z-50 text-xs text-slate-300 bg-slate-900/80 border border-slate-700 rounded px-2 py-1 hover:bg-slate-800"
      >
        ← Dashboard
      </a>
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
