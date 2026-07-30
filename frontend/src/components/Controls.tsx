import { useState } from 'react'

interface ControlsProps {
  onSearch: (q: string) => void
  onEdgesToggle: (show: boolean) => void
  onProposedToggle: (show: boolean) => void
  onEdgeOpacity: (v: number) => void
  onHueOffset: (v: number) => void
  onDensity: (v: number) => void
}

const sliderStyle = { width: '100%', accentColor: '#7ec8e3', cursor: 'pointer' }
const rowStyle = { display: 'flex', flexDirection: 'column' as const, gap: 2 }

export function Controls({ onSearch, onEdgesToggle, onProposedToggle, onEdgeOpacity, onHueOffset, onDensity }: ControlsProps) {
  const [query, setQuery] = useState('')
  const [showEdges, setShowEdges] = useState(true)
  const [showProposed, setShowProposed] = useState(false)
  const [edgeOp, setEdgeOp] = useState(0.6)
  const [hue, setHue] = useState(0)
  const [dens, setDens] = useState(50)

  function handleEdgesToggle(checked: boolean) {
    setShowEdges(checked)
    onEdgesToggle(checked)
    if (!checked) {
      onProposedToggle(false)
    } else {
      onProposedToggle(showProposed)
    }
  }

  function handleProposedToggle(checked: boolean) {
    setShowProposed(checked)
    onProposedToggle(checked)
  }

  return (
    <div
      style={{
        position: 'absolute',
        top: 10,
        right: 10,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        zIndex: 10,
        pointerEvents: 'auto',
        background: 'rgba(15,26,48,0.85)',
        padding: 10,
        borderRadius: 6,
        minWidth: 180,
      }}
    >
      <input
        type="search"
        placeholder="Search nodes..."
        value={query}
        onChange={e => {
          setQuery(e.target.value)
          onSearch(e.target.value)
        }}
        style={{ background: '#16213e', color: '#fff', border: '1px solid #0f3460', borderRadius: 4, padding: '4px 6px' }}
      />

      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        <label style={{ color: '#e0e0e0', fontSize: 13, display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <span style={{
            display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
            width: 14, height: 14, border: '1px solid #7ec8e3', borderRadius: 3,
            background: showEdges ? '#7ec8e3' : 'transparent',
            fontSize: 10, color: '#0f1a30', fontWeight: 'bold',
          }}>
            {showEdges ? '✓' : ''}
          </span>
          <input type="checkbox" checked={showEdges} onChange={e => handleEdgesToggle(e.target.checked)} style={{ display: 'none' }} />
          Edges
        </label>
        <label style={{ color: '#e0e0e0', fontSize: 13, display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}>
          <span style={{
            display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
            width: 14, height: 14, border: '1px solid #e8a33d', borderRadius: 3,
            background: showProposed ? '#e8a33d' : 'transparent',
            fontSize: 10, color: '#0f1a30', fontWeight: 'bold',
          }}>
            {showProposed ? '✓' : ''}
          </span>
          <input type="checkbox" checked={showProposed} onChange={e => handleProposedToggle(e.target.checked)} style={{ display: 'none' }} />
          Weak links
        </label>
      </div>

      <div style={rowStyle}>
        <label style={{ color: '#aaa', fontSize: 11 }}>Edge opacity</label>
        <input type="range" min={0} max={1} step={0.05} value={edgeOp}
          onChange={e => { const v = +e.target.value; setEdgeOp(v); onEdgeOpacity(v) }}
          style={sliderStyle} />
      </div>

      <div style={rowStyle}>
        <label style={{ color: '#aaa', fontSize: 11 }}>Hue offset</label>
        <input type="range" min={0} max={360} step={1} value={hue}
          onChange={e => { const v = +e.target.value; setHue(v); onHueOffset(v) }}
          style={sliderStyle} />
      </div>

      <div style={rowStyle}>
        <label style={{ color: '#aaa', fontSize: 11 }}>Density</label>
        <input type="range" min={0} max={100} step={1} value={dens}
          onChange={e => { const v = +e.target.value; setDens(v); onDensity(v) }}
          style={sliderStyle} />
      </div>
    </div>
  )
}
