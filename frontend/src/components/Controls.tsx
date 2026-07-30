import { useState } from 'react'

interface ControlsProps {
  onSearch: (q: string) => void
  onEdgesToggle: (show: boolean) => void
  onProposedToggle: (show: boolean) => void
}

export function Controls({ onSearch, onEdgesToggle, onProposedToggle }: ControlsProps) {
  const [query, setQuery] = useState('')
  const [showEdges, setShowEdges] = useState(true)
  const [showProposed, setShowProposed] = useState(false)

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
        gap: 5,
        zIndex: 10,
        pointerEvents: 'auto',
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
        style={{ background: '#16213e', color: '#fff', border: '1px solid #0f3460', borderRadius: 4, padding: 4 }}
      />
      <label style={{ color: '#e0e0e0', fontSize: 13 }}>
        <input type="checkbox" checked={showEdges} onChange={e => handleEdgesToggle(e.target.checked)} /> Edges
      </label>
      <label style={{ color: '#e0e0e0', fontSize: 13 }}>
        <input type="checkbox" checked={showProposed} onChange={e => handleProposedToggle(e.target.checked)} /> Weak links (proposed)
      </label>
    </div>
  )
}
