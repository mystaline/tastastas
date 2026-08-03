export function getProjectFromPath(): string | null {
  const m = window.location.pathname.match(/^\/graph\/([^/]+)\/?$/)
  return m ? m[1] : null
}
