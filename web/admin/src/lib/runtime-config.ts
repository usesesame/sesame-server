function configuredOrigin(name: string, value: string | undefined): string {
  const configured = value?.trim()
  if (!configured) {
    if (import.meta.env.DEV && typeof window !== 'undefined') return window.location.origin
    throw new Error(`${name} must be configured at build time.`)
  }

  let url: URL
  try {
    url = new URL(configured)
  } catch {
    throw new Error(`${name} must be an absolute origin.`)
  }
  const loopbackHTTP = url.protocol === 'http:'
    && (url.hostname === 'localhost' || url.hostname === '127.0.0.1' || url.hostname === '[::1]')
  if ((url.protocol !== 'https:' && !loopbackHTTP)
    || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error(`${name} must be an HTTPS origin (HTTP is allowed only for loopback development).`)
  }
  return url.origin
}

export const apiURL = configuredOrigin('VITE_SESAME_API_URL', import.meta.env.VITE_SESAME_API_URL)
