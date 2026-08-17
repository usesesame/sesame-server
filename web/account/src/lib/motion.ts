/**
 * Scroll motion for the public site.
 *
 * The pages are prerendered, so nothing here may decide whether content is
 * visible. Without JavaScript, without IntersectionObserver, or with reduced
 * motion requested, the page simply renders.
 */

export function prefersReducedMotion(): boolean {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
}

type QueuedReveal = { node: HTMLElement; delay: number }
const revealQueue = new Map<HTMLElement, number>()
let revealFrame = 0

function flushRevealQueue() {
  revealFrame = 0
  const items: QueuedReveal[] = [...revealQueue].map(([node, delay]) => ({ node, delay }))
  revealQueue.clear()
  items.sort((a, b) => {
    const first = a.node.getBoundingClientRect()
    const second = b.node.getBoundingClientRect()
    return first.top - second.top || first.left - second.left
  })
  items.forEach(({ node, delay }, index) => {
    node.style.setProperty('--reveal-delay', `${delay + index * 70}ms`)
    node.classList.add('is-revealed')
  })
}

function queueReveal(node: HTMLElement, delay: number) {
  revealQueue.set(node, delay)
  if (!revealFrame) revealFrame = requestAnimationFrame(flushRevealQueue)
}

/**
 * Fades and lifts an element into place the first time it scrolls into view.
 * `delay` staggers siblings so a grid arrives as a wave rather than a jump.
 */
export function reveal(node: HTMLElement, delay = 0) {
  if (typeof IntersectionObserver === 'undefined' || prefersReducedMotion()) return
  node.classList.add('reveal')
  let revealed = false
  let fallback = 0

  const show = () => {
    if (revealed) return
    revealed = true
    window.clearInterval(fallback)
    queueReveal(node, delay)
    observer.disconnect()
  }

  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue
        show()
      }
    },
    { rootMargin: '0px 0px -10% 0px', threshold: 0.05 },
  )
  observer.observe(node)

  fallback = window.setInterval(() => {
    const bounds = node.getBoundingClientRect()
    if (bounds.top <= window.innerHeight * 0.94) show()
  }, 250)

  return {
    destroy: () => {
      window.clearInterval(fallback)
      revealQueue.delete(node)
      observer.disconnect()
    },
  }
}

/**
 * Reports whether the page has scrolled past the header's resting state, so the
 * header can settle onto the content instead of floating over it unchanged.
 */
export function watchScroll(onChange: (scrolled: boolean) => void) {
  if (typeof window === 'undefined') return () => {}

  let scrolled: boolean | null = null
  let queued = false
  const measure = () => {
    queued = false
    const next = window.scrollY > 12
    if (next === scrolled) return
    scrolled = next
    onChange(scrolled)
  }
  const onScroll = () => {
    if (queued) return
    queued = true
    requestAnimationFrame(measure)
  }

  measure()
  window.addEventListener('scroll', onScroll, { passive: true })
  return () => window.removeEventListener('scroll', onScroll)
}
