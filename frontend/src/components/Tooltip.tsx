import { createPortal } from 'react-dom'
import type { TooltipState } from '../hooks/useForceSimulation'
import type { GraphNode, GraphEdge } from '../types/graph'

interface TooltipProps {
  state: TooltipState | null
  onPin: (id: string | null) => void
}

export function Tooltip({ state, onPin }: TooltipProps) {
  if (!state || !state.visible) return null

  return createPortal(
    <div
      onClick={() => {
        if (state.type === 'node') onPin((state.data as GraphNode).id)
      }}
      className="absolute bg-slate-800 border border-slate-700 rounded-lg p-3 text-sm max-w-[400px] z-[100] shadow-xl text-slate-200 leading-relaxed pointer-events-auto"
      style={{ left: state.x, top: state.y }}
    >
      {state.type === 'node'
        ? <NodeTooltip node={state.data as GraphNode} connectedEdges={state.connectedEdges} />
        : <EdgeTooltip edge={state.data as GraphEdge} />
      }
    </div>,
    document.body,
  )
}

function NodeTooltip({ node, connectedEdges }: { node: GraphNode; connectedEdges: GraphEdge[] }) {
  return (
    <>
      <div className="font-semibold text-sm text-slate-200 mb-1">{node.title || node.id}</div>
      <div className="text-slate-400 text-xs">
        Group: <span className="text-cyan-400">{node.group}</span> · Type: <span className="text-cyan-400">{node.type}</span> · Degree: <span className="text-cyan-400">{node.weight}</span>
        <br />
        ID: <span className="text-cyan-400 text-[11px]">{node.id}</span>
      </div>
      {connectedEdges.length === 0 ? (
        <div className="mt-1.5 text-slate-500 text-[11px]">No connected edges</div>
      ) : (
        <div className="mt-1.5 max-h-[300px] overflow-y-auto text-[11px]">
          <div className="text-slate-500 mb-1">Edges ({connectedEdges.length}):</div>
          {connectedEdges.map((e, i) => (
            <div
              key={i}
              className="py-0.5 border-b border-white/5 whitespace-nowrap overflow-hidden text-ellipsis"
            >
              <span className="text-cyan-400">{node.id === e.source ? e.target : e.source}</span>
              <span className="text-slate-600"> → </span>
              <span className="text-slate-400">{e.edge_type}</span>
              <span className="text-slate-600"> {e.confidence.toFixed(2)}</span>
            </div>
          ))}
        </div>
      )}
    </>
  )
}

function EdgeTooltip({ edge }: { edge: GraphEdge }) {
  return (
    <>
      <div className="font-semibold text-sm text-slate-200 mb-1">
        <span className="text-cyan-400">{edge.source}</span> → <span className="text-cyan-400">{edge.target}</span>
      </div>
      <div className="text-slate-400 text-xs">
        Type: <span className="text-cyan-400">{edge.edge_type}</span> · Confidence: <span className="text-cyan-400">{edge.confidence.toFixed(4)}</span>
      </div>
    </>
  )
}
