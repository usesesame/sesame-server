const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const dateOnly = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' })
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000],
  ['month', 2_592_000],
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
]

export function formatDateTime(value?: string | null): string {
  const date = value ? new Date(value) : null
  return date && !Number.isNaN(date.getTime()) ? dateTime.format(date) : 'Never'
}

export function formatDate(value?: string | null): string {
  const date = value ? new Date(value) : null
  return date && !Number.isNaN(date.getTime()) ? dateOnly.format(date) : 'Unknown'
}

export function formatRelative(value: string | null | undefined, now = Date.now()): string {
  const date = value ? new Date(value) : null
  if (!date || Number.isNaN(date.getTime())) return 'Never'
  const seconds = Math.round((date.getTime() - now) / 1000)
  if (Math.abs(seconds) < 45) return 'Just now'
  for (const [unit, size] of UNITS) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit)
  }
  return relative.format(Math.round(seconds / 60), 'minute')
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return 'Unknown'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let index = 0
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[index]}`
}

export function formatCountdown(milliseconds: number): string {
  const total = Math.max(0, Math.ceil(milliseconds / 1000))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  const pad = (value: number) => String(value).padStart(2, '0')
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`
}

export function isOlderVersion(version: string, minimum: string): boolean {
  const parse = (value: string) => /^v?(\d+)\.(\d+)\.(\d+)/.exec(value)?.slice(1).map(Number)
  const current = parse(version)
  const floor = parse(minimum)
  if (!current || !floor) return false
  for (let index = 0; index < 3; index += 1) {
    if (current[index] !== floor[index]) return current[index] < floor[index]
  }
  return false
}

export function pluralize(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`
}

export function deviceDetail(device: { platform?: string; architecture?: string; appVersion?: string }): string {
  const platform = [device.platform, device.architecture].filter(Boolean).join(' ')
  return [platform, device.appVersion ? `Sesame ${device.appVersion}` : ''].filter(Boolean).join(' · ')
}
