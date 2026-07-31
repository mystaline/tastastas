import { useState } from 'react'
import { SIM_DEFAULTS } from '../hooks/useForceSimulation'

interface ControlsProps {
  onSearch: (q: string) => void
  onEdgesToggle: (show: boolean) => void
  onProposedToggle: (show: boolean) => void
  onEdgeOpacity: (v: number) => void
  onHueOffset: (v: number) => void
  onDensity: (v: number) => void
  initialShowEdges?: boolean
  initialEdgeOpacity?: number
  initialHueOffset?: number
  initialDensity?: number
}

export function Controls({
  onSearch,
  onEdgesToggle,
  onProposedToggle,
  onEdgeOpacity,
  onHueOffset,
  onDensity,
  initialShowEdges = SIM_DEFAULTS.showEdges,
  initialEdgeOpacity = SIM_DEFAULTS.edgeOpacity,
  initialHueOffset = SIM_DEFAULTS.hueOffset,
  initialDensity = SIM_DEFAULTS.density,
}: ControlsProps) {
  const [query, setQuery] = useState('')
  const [showEdges, setShowEdges] = useState(initialShowEdges)
  const [showProposed, setShowProposed] = useState(false)
  const [edgeOp, setEdgeOp] = useState(initialEdgeOpacity)
  const [hue, setHue] = useState(initialHueOffset)
  const [dens, setDens] = useState(initialDensity)

  function handleEdgesToggle(checked: boolean) {
    setShowEdges(checked)
    onEdgesToggle(checked)
    if (!checked) {
      setShowProposed(false)
      onProposedToggle(false)
    } else {
      onProposedToggle(showProposed)
    }
  }

  return (
    <div className="fixed top-3 right-3 z-10 bg-slate-900/85 backdrop-blur p-3 rounded-lg min-w-[198px] flex flex-col gap-2 text-slate-200 text-sm">
      <input
        type="search"
        placeholder="Search nodes..."
        value={query}
        onChange={e => {
          setQuery(e.target.value)
          onSearch(e.target.value)
        }}
        className="bg-slate-800 text-white border border-slate-700 rounded px-1.5 py-1 text-xs outline-none focus:border-cyan-500"
      />

      <div className="flex flex-col gap-1">
        <label className="flex items-center gap-1.5 text-slate-200 text-xs cursor-pointer select-none">
          <span
            style={{
              display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
              width: 14, height: 14, border: '1px solid #22d3ee',
              borderRadius: 3, background: showEdges ? '#22d3ee' : 'transparent',
              fontSize: 10, color: '#0f172a', fontWeight: 700,
            }}
          >
            {showEdges ? '✓' : ''}
          </span>
          <input
            type="checkbox"
            checked={showEdges}
            onChange={e => handleEdgesToggle(e.target.checked)}
            className="hidden"
          />
          Edges
        </label>

        <label className="flex items-center gap-1.5 text-slate-200 text-xs cursor-pointer select-none">
          <span
            style={{
              display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
              width: 14, height: 14, border: '1px solid #f59e0b',
              borderRadius: 3, background: showProposed ? '#f59e0b' : 'transparent',
              fontSize: 10, color: '#0f172a', fontWeight: 700,
            }}
          >
            {showProposed ? '✓' : ''}
          </span>
          <input
            type="checkbox"
            checked={showProposed}
            onChange={e => {
              const v = e.target.checked
              setShowProposed(v)
              onProposedToggle(v)
            }}
            className="hidden"
          />
          Weak links
        </label>
      </div>

      <div className="flex flex-col gap-0.5">
        <label className="text-slate-400 text-[11px]">Edge opacity</label>
        <input
          type="range"
          min={0}
          max={1}
          step={0.05}
          value={edgeOp}
          onChange={e => { const v = +e.target.value; setEdgeOp(v); onEdgeOpacity(v) }}
          className="w-full accent-cyan-400 cursor-pointer"
        />
      </div>

      <div className="flex flex-col gap-0.5">
        <label className="text-slate-400 text-[11px]">Hue offset</label>
        <input
          type="range"
          min={0}
          max={360}
          step={1}
          value={hue}
          onChange={e => { const v = +e.target.value; setHue(v); onHueOffset(v) }}
          className="w-full accent-cyan-400 cursor-pointer"
        />
      </div>

      <div className="flex flex-col gap-0.5">
        <label className="text-slate-400 text-[11px]">Density</label>
        <input
          type="range"
          min={0}
          max={100}
          step={1}
          value={dens}
          onChange={e => { const v = +e.target.value; setDens(v); onDensity(v) }}
          className="w-full accent-cyan-400 cursor-pointer"
        />
      </div>
    </div>
  )
}
