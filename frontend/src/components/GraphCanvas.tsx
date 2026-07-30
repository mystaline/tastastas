import { type RefObject, useEffect } from 'react'

export function GraphCanvas({ svgRef }: { svgRef: RefObject<SVGSVGElement | null> }) {
  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return
    const resize = () => {
      svg.setAttribute('width', String(window.innerWidth))
      svg.setAttribute('height', String(window.innerHeight))
    }
    window.addEventListener('resize', resize)
    return () => window.removeEventListener('resize', resize)
  }, [svgRef])

  return (
    <svg
      ref={svgRef}
      style={{ display: 'block', width: '100vw', height: '100vh', background: '#0f1a30' }}
    />
  )
}
