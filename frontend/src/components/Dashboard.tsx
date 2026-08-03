import { useEffect, useState } from 'react'

interface ProjectInfo {
  project_id: string
  project_name?: string
  repository_url?: string
  stage?: string
  effective_project_id?: string
  node_count: number
  edge_count: number
}

interface ProjectGroup {
  baseProjectID: string
  projectName: string
  repositoryURL: string
  stages: ProjectInfo[]
  totals: { nodes: number; edges: number }
}

const DEFAULT_MAX_EDGES = 2000

export function Dashboard() {
  const [groups, setGroups] = useState<ProjectGroup[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [maxEdges, setMaxEdges] = useState<number>(() => {
    const v = new URLSearchParams(window.location.search).get('max_edges')
    const n = v ? parseInt(v, 10) : NaN
    return Number.isFinite(n) && n > 0 ? n : DEFAULT_MAX_EDGES
  })

  useEffect(() => {
    fetch('/api/projects', { headers: { Accept: 'application/json' } })
      .then(res => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json() as Promise<{ projects: ProjectInfo[] }>
      })
      .then(d => {
        const grouped = new Map<string, ProjectGroup>()
        for (const p of d.projects ?? []) {
          const base = p.effective_project_id
            ? p.effective_project_id.split('::')[0]
            : p.project_id
          const g =
            grouped.get(base) ??
            {
              baseProjectID: base,
              projectName: p.project_name ?? base,
              repositoryURL: p.repository_url ?? '',
              stages: [],
              totals: { nodes: 0, edges: 0 },
            }
          g.stages.push(p)
          g.totals.nodes += p.node_count
          g.totals.edges += p.edge_count
          if (p.project_name && !g.projectName) g.projectName = p.project_name
          if (p.repository_url && !g.repositoryURL) g.repositoryURL = p.repository_url
          grouped.set(base, g)
        }
        setGroups([...grouped.values()])
      })
      .catch(err => {
        setError(err instanceof Error ? err.message : 'Fetch failed')
      })
  }, [])

  const linkHref = (stage: ProjectInfo): string => {
    const base = encodeURIComponent(stage.project_id)
    const params = new URLSearchParams()
    if (stage.stage) params.set('stage', stage.stage)
    if (maxEdges !== DEFAULT_MAX_EDGES) params.set('max_edges', String(maxEdges))
    const qs = params.toString()
    return `/graph/${base}/${qs ? `?${qs}` : ''}`
  }

  return (
    <div className="min-h-screen bg-slate-950 text-slate-100 p-8">
      <header className="max-w-4xl mx-auto mb-6">
        <h1 className="text-2xl font-semibold mb-1">Tastastas Graph Dashboard</h1>
        <p className="text-slate-400 text-sm">Pick a project to explore its knowledge graph.</p>
      </header>

      <div className="max-w-4xl mx-auto mb-6 flex items-center gap-3">
        <label htmlFor="max-edges" className="text-sm text-slate-300">
          Max edges
        </label>
        <input
          id="max-edges"
          type="number"
          min={1}
          value={maxEdges}
          onChange={e => {
            const n = parseInt(e.target.value, 10)
            if (Number.isFinite(n) && n > 0) setMaxEdges(n)
          }}
          className="w-28 bg-slate-900 border border-slate-700 rounded px-2 py-1 text-sm"
        />
        <span className="text-xs text-slate-500">applied to every link</span>
      </div>

      <main className="max-w-4xl mx-auto">
        {error && <div className="text-red-400">Error: {error}</div>}
        {!groups && !error && <div className="text-slate-500">Loading projects…</div>}
        {groups && groups.length === 0 && (
          <div className="text-slate-500">No projects onboarded yet.</div>
        )}
        {groups && groups.length > 0 && (
          <ul className="space-y-4">
            {groups.map(g => (
              <li
                key={g.baseProjectID}
                className="bg-slate-900/60 border border-slate-800 rounded p-4"
              >
                <div className="flex items-baseline justify-between gap-3">
                  <h2 className="text-lg font-medium">{g.projectName}</h2>
                  <code className="text-xs text-slate-500">{g.baseProjectID}</code>
                </div>
                {g.repositoryURL && (
                  <a
                    href={g.repositoryURL}
                    target="_blank"
                    rel="noreferrer"
                    className="text-xs text-sky-400 hover:underline"
                  >
                    {g.repositoryURL}
                  </a>
                )}
                <div className="text-xs text-slate-500 mt-1">
                  {g.totals.nodes} nodes · {g.totals.edges} edges
                </div>
                <ul className="mt-3 space-y-1">
                  {g.stages.map(s => (
                    <li key={s.effective_project_id ?? s.project_id}>
                      <a
                        href={linkHref(s)}
                        className="block px-3 py-2 rounded hover:bg-slate-800 border border-transparent hover:border-slate-700"
                      >
                        <div className="flex items-baseline justify-between">
                          <span className="text-sm">
                            {s.stage ? `stage: ${s.stage}` : 'no stage'}
                          </span>
                          <span className="text-xs text-slate-500">
                            {s.node_count} nodes · {s.edge_count} edges
                          </span>
                        </div>
                      </a>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        )}
      </main>
    </div>
  )
}
