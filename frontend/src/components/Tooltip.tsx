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
      id="graph-tooltip"
      onClick={() => {
        if (state.type === 'node') onPin((state.data as GraphNode).id)
      }}
      style={{
        position: 'absolute',
        left: state.x,
        top: state.y,
        background: '#16213e',
        border: '1px solid #0f3460',
        borderRadius: 6,
        padding: '10px 14px',
        fontSize: 13,
        pointerEvents: 'auto',
        maxWidth: 400,
        zIndex: 100,
        boxShadow: '0 4px 20px rgba(0,0,0,.4)',
        color: '#e0e0e0',
        lineHeight: 1.4,
      }}
    >
      {state.type === 'node' ? <NodeTooltip node={state.data as GraphNode} connectedEdges={state.connectedEdges} /> : <EdgeTooltip edge={state.data as GraphEdge} />}
    </div>,
    document.body,
  )
}

function NodeTooltip({ node, connectedEdges }: { node: GraphNode; connectedEdges: GraphEdge[] }) {
  return (
    <>
      <div style={{ fontWeight: 600, fontSize: 14, color: '#e0e0e0', marginBottom: 4 }}>{node.title || node.id}</div>
      <div style={{ color: '#a0a0b0', fontSize: 12 }}>
        Group: <span style={{ color: '#7ec8e3' }}>{node.group}</span> · Type: <span style={{ color: '#7ec8e3' }}>{node.type}</span> · Degree: <span style={{ color: '#7ec8e3' }}>{node.weight}</span>
        <br />
        ID: <span style={{ color: '#7ec8e3', fontSize: 11 }}>{node.id}</span>
      </div>
      {connectedEdges.length === 0 ? (
        <div style={{ marginTop: 6, color: '#808090', fontSize: 11 }}>No connected edges</div>
      ) : (
        <div style={{ marginTop: 6, maxHeight: 300, overflowY: 'auto', fontSize: 11 }}>
          <div style={{ color: '#808090', marginBottom: 4 }}>Edges ({connectedEdges.length}):</div>
          {connectedEdges.map((e, i) => (
            <div
              key={i}
              style={{
                padding: '2px 0',
                borderBottom: '1px solid rgba(255,255,255,.05)',
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              <span style={{ color: '#7ec8e3' }}>{node.id === e.source ? e.target : e.source}</span>
              <span style={{ color: '#606080' }}> → </span>
              <span style={{ color: '#a0a0b0' }}>{e.edge_type}</span>
              <span style={{ color: '#606080' }}> {e.confidence.toFixed(2)}</span>
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
      <div style={{ fontWeight: 600, fontSize: 14, color: '#e0e0e0', marginBottom: 4 }}>
        <span style={{ color: '#7ec8e3' }}>{edge.source}</span> → <span style={{ color: '#7ec8e3' }}>{edge.target}</span>
      </div>
      <div className="meta" style={{ color: '#a0a0b0', fontSize: 12 }}>
        Type: <span style={{ color: '#7ec8e3' }}>{edge.edge_type}</span> · Confidence: <span style={{ color: '#7ec8e3' }}>{edge.confidence.toFixed(4)}</span>
      </div>
    </>
  )
}
