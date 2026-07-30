import { useEffect, useRef, type RefObject } from 'react'

interface GraphCanvasProps {
  svgRef: RefObject<SVGSVGElement | null>
  onResize?: (width: number, height: number) => void
}

export function GraphCanvas({ svgRef, onResize }: GraphCanvasProps) {
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const svg = svgRef.current
    if (!svg) return

    const resize = () => {
      svg.setAttribute('width', String(window.innerWidth))
      svg.setAttribute('height', String(window.innerHeight))
      if (onResize) {
        if (debounceRef.current) clearTimeout(debounceRef.current)
        debounceRef.current = setTimeout(() => {
          onResize(window.innerWidth, window.innerHeight)
        }, 300)
      }
    }

    window.addEventListener('resize', resize)
    return () => {
      window.removeEventListener('resize', resize)
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [svgRef, onResize])

  return (
    <svg
      ref={svgRef}
      className="w-screen h-screen block bg-slate-950"
    />
  )
}
