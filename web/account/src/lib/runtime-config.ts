function developmentOrigin(): string | undefined {
  if (!import.meta.env.DEV || typeof window === 'undefined') return undefined
  return window.location.origin
}

function configuredOrigin(name: string, value: string | undefined): string {
  const configured = value?.trim()
  if (!configured) {
    const localOrigin = developmentOrigin()
    if (localOrigin) return localOrigin
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

/**
 * Deployment-owned origins. No production endpoint is compiled into the
 * portal, so a self-hosted deployment points these at its own server.
 */
export const apiBaseURL = configuredOrigin('VITE_SESAME_API_URL', import.meta.env.VITE_SESAME_API_URL)

/** Where the public site lives. The portal only ever links out to it. */
export const siteOrigin = configuredOrigin('VITE_SESAME_SITE_ORIGIN', import.meta.env.VITE_SESAME_SITE_ORIGIN)
