import { execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const root = path.resolve(import.meta.dirname, '..')

export const exceptionsPath = path.join(root, 'scripts', 'advisory-exceptions.json')

const MAX_EXCEPTION_DAYS = 30
const FAILING_NPM_SEVERITIES = new Set(['high', 'critical'])
const RESOURCE_EXHAUSTION_WEAKNESSES = new Set(['CWE-1333', 'CWE-400', 'CWE-405', 'CWE-407', 'CWE-770', 'CWE-834'])
const BUILD_RECORD_DIRECTORY = path.join('node_modules', '.cache', 'shipped-packages')
const buildName = /^[a-z0-9][a-z0-9-]*$/
const sha256 = /^[0-9a-f]{64}$/
const isoDate = /^\d{4}-\d{2}-\d{2}$/
const advisoryId = /^GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}$/
const dayNumber = (value) => Date.parse(`${value}T00:00:00Z`) / 86_400_000
const isCalendarDate = (value) =>
  isoDate.test(value) && Number.isFinite(dayNumber(value)) && new Date(`${value}T00:00:00Z`).toISOString().startsWith(value)

export const validateExceptions = (document, today) => {
  if (!document || typeof document !== 'object' || Array.isArray(document) || !Array.isArray(document.entries)) {
    throw new Error('The advisory exceptions file must hold an "entries" array.')
  }
  const seen = new Set()
  for (const entry of document.entries) {
    const shown = JSON.stringify(entry)
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      throw new Error(`Every advisory exception must be an object: ${shown}`)
    }
    if (Object.keys(entry).sort().join(',') !== 'expires,id,package,reason,reviewed') {
      throw new Error(`Every advisory exception has exactly an id, a package, a reason, a reviewed date and an expiry date: ${shown}`)
    }
    if (typeof entry.id !== 'string' || !advisoryId.test(entry.id)) {
      throw new Error(`An advisory exception must name a GHSA advisory: ${shown}`)
    }
    for (const field of ['package', 'reason']) {
      if (typeof entry[field] !== 'string' || entry[field].trim().length === 0) {
        throw new Error(`An advisory exception needs a ${field}: ${shown}`)
      }
    }
    if (!isCalendarDate(entry.reviewed) || !isCalendarDate(entry.expires)) {
      throw new Error(`An advisory exception needs a reviewed date and an expiry date as YYYY-MM-DD: ${shown}`)
    }
    if (dayNumber(entry.reviewed) > dayNumber(today)) {
      throw new Error(`An advisory exception cannot be reviewed after today: ${shown}`)
    }
    const days = dayNumber(entry.expires) - dayNumber(entry.reviewed)
    if (!(days > 0 && days <= MAX_EXCEPTION_DAYS)) {
      throw new Error(`An advisory exception must expire within ${MAX_EXCEPTION_DAYS} days after it was reviewed: ${shown}`)
    }
    if (entry.expires < today) {
      throw new Error(`The advisory exception for ${entry.id} in ${entry.package} expired on ${entry.expires}. Fix the dependency or review the exception again.`)
    }
    const key = `${entry.package}|${entry.id}`
    if (seen.has(key)) throw new Error(`The advisory exception for ${entry.id} in ${entry.package} is listed twice.`)
    seen.add(key)
  }
  return document.entries
}

export const loadExceptions = (today, file = exceptionsPath) =>
  validateExceptions(JSON.parse(readFileSync(file, 'utf8')), today)

export const npmFindings = (report) => {
  if (!report || typeof report !== 'object' || report.error || !report.vulnerabilities || typeof report.vulnerabilities !== 'object') {
    const summary = report?.error?.summary ? `: ${report.error.summary}` : ''
    throw new Error(`npm audit did not return a report${summary}.`)
  }
  const findings = new Map()
  for (const vulnerability of Object.values(report.vulnerabilities)) {
    for (const source of Array.isArray(vulnerability?.via) ? vulnerability.via : []) {
      if (!source || typeof source !== 'object' || !FAILING_NPM_SEVERITIES.has(source.severity)) continue
      const id = String(source.url ?? '').match(/GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}/)?.[0] ?? `npm advisory ${source.source}`
      const key = `${source.name}|${id}`
      const nodes = vulnerability.name === source.name && Array.isArray(vulnerability.nodes) ? vulnerability.nodes : []
      findings.set(key, {
        id,
        package: source.name,
        severity: source.severity,
        title: source.title ?? '',
        url: source.url ?? '',
        cwe: Array.isArray(source.cwe) ? [...source.cwe] : [],
        nodes: [...new Set([...(findings.get(key)?.nodes ?? []), ...nodes])],
      })
    }
  }
  return [...findings.values()]
}

export const onlyResourceExhaustion = (finding) =>
  Array.isArray(finding.cwe) && finding.cwe.length > 0 && finding.cwe.every((weakness) => RESOURCE_EXHAUSTION_WEAKNESSES.has(weakness))

const packageNameAt = (installPath) => {
  const segments = String(installPath).split('node_modules/').at(-1).split('/')
  return segments[0].startsWith('@') ? segments.slice(0, 2).join('/') : segments[0]
}

export const installedCopies = (lockfile, finding) => {
  const packages = lockfile.packages
  const paths = new Set(finding.nodes ?? [])
  for (const [installPath, entry] of Object.entries(packages)) {
    if (!installPath.includes('node_modules/')) continue
    if (packageNameAt(installPath) === finding.package || entry?.name === finding.package) paths.add(installPath)
  }
  return [...paths].map((installPath) => ({ path: installPath, entry: Object.hasOwn(packages, installPath) ? packages[installPath] : undefined }))
}

export const bundledPackages = (projectDirectory, lockfileBytes) => {
  let manifest
  try {
    manifest = JSON.parse(readFileSync(path.join(projectDirectory, 'package.json'), 'utf8'))
  } catch {
    return { unknown: 'package.json could not be read' }
  }
  const builds = manifest?.shippedBuilds
  if (!Array.isArray(builds) || builds.length === 0 || !builds.every((build) => typeof build === 'string' && buildName.test(build))) {
    return { unknown: 'package.json names no shipped builds' }
  }
  const lockfileHash = createHash('sha256').update(lockfileBytes).digest('hex')
  const packages = new Set()
  for (const build of builds) {
    let record
    try {
      record = JSON.parse(readFileSync(path.join(projectDirectory, BUILD_RECORD_DIRECTORY, `${build}.json`), 'utf8'))
    } catch {
      return { unknown: `no build has recorded what the ${build} build bundles` }
    }
    if (!record || typeof record.lockfile !== 'string' || !sha256.test(record.lockfile) || !Array.isArray(record.packages) || !record.packages.every((entry) => typeof entry === 'string')) {
      return { unknown: `the record of the ${build} build is malformed` }
    }
    if (record.lockfile !== lockfileHash) {
      return { unknown: `the record of the ${build} build was made from a different package-lock.json` }
    }
    for (const entry of record.packages) packages.add(entry)
  }
  return { packages }
}

export const shippedState = (finding, lockfile, bundled) => {
  if (!lockfile || typeof lockfile.packages !== 'object' || lockfile.packages === null) {
    return { state: 'unknown', reason: 'package-lock.json could not be read' }
  }
  const copies = installedCopies(lockfile, finding)
  if (copies.length === 0) return { state: 'unknown', reason: `package-lock.json has no installed copy of ${finding.package}` }
  const missing = copies.find(({ entry }) => !entry || typeof entry !== 'object')
  if (missing) return { state: 'unknown', reason: `package-lock.json has no entry for ${missing.path}` }
  const runtime = copies.find(({ entry }) => entry.dev !== true)
  if (runtime) return { state: 'shipped', reason: `${runtime.path} is not a development dependency` }
  if (!bundled || !(bundled.packages instanceof Set)) {
    return { state: 'unknown', reason: bundled?.unknown ?? 'no build has recorded what it bundles' }
  }
  const inBuild = copies.find(({ path: installPath }) => bundled.packages.has(installPath))
  if (inBuild) return { state: 'shipped', reason: `a shipped build bundles ${inBuild.path}` }
  if ([...bundled.packages].some((entry) => packageNameAt(entry) === finding.package)) {
    return { state: 'shipped', reason: `a shipped build bundles a package named ${finding.package}` }
  }
  return { state: 'never', reason: `every copy of ${finding.package} is a development dependency and no shipped build bundles it` }
}

export const applyBuildToolRule = (findings, lockfile, bundled) => {
  const buildOnly = []
  const remaining = []
  for (const finding of findings) {
    if (!onlyResourceExhaustion(finding)) {
      remaining.push(finding)
      continue
    }
    const { state, reason } = shippedState(finding, lockfile, bundled)
    if (state === 'never') buildOnly.push({ ...finding, reason })
    else remaining.push({ ...finding, reason: `it only describes resource exhaustion, but ${reason}` })
  }
  return { buildOnly, remaining }
}

export const evaluateFindings = (findings, exceptions) => {
  const matches = (finding, entry) => entry.id === finding.id && entry.package === finding.package
  const accepted = []
  const blocking = []
  for (const finding of findings) {
    const entry = exceptions.find((candidate) => matches(finding, candidate))
    if (entry) accepted.push({ ...finding, expires: entry.expires })
    else blocking.push(finding)
  }
  const unused = exceptions.filter((entry) => !findings.some((finding) => matches(finding, entry)))
  return { accepted, blocking, unused }
}

const run = promisify(execFile)

const reportFrom = async (command, args, cwd) => {
  try {
    const { stdout } = await run(command, args, { cwd, maxBuffer: 64 * 1024 * 1024 })
    return JSON.parse(stdout)
  } catch (error) {
    if (typeof error.stdout === 'string' && error.stdout.trim().length > 0) return JSON.parse(error.stdout)
    throw error
  }
}

const readLockfile = (projectDirectory) => {
  try {
    const bytes = readFileSync(path.join(projectDirectory, 'package-lock.json'))
    return { bytes, lockfile: JSON.parse(bytes.toString('utf8')) }
  } catch {
    return { bytes: Buffer.alloc(0), lockfile: null }
  }
}

const main = async () => {
  const today = new Date().toISOString().slice(0, 10)
  const exceptions = loadExceptions(today)
  const projectDirectory = process.cwd()
  const findings = npmFindings(await reportFrom('npm', ['audit', '--json'], projectDirectory))
  const { bytes, lockfile } = readLockfile(projectDirectory)
  const { buildOnly, remaining } = applyBuildToolRule(findings, lockfile, bundledPackages(projectDirectory, bytes))
  const { accepted, blocking, unused } = evaluateFindings(remaining, exceptions)
  for (const finding of buildOnly) {
    console.log(`Not failing on ${finding.id} in ${finding.package}: it only describes resource exhaustion (${finding.cwe.join(', ')}), and ${finding.reason}.`)
  }
  for (const finding of accepted) {
    console.log(`Accepted until ${finding.expires}: ${finding.id} in ${finding.package}.`)
  }
  for (const entry of unused) {
    console.log(`No longer needed, so the exception can be removed: ${entry.id} in ${entry.package}.`)
  }
  for (const finding of blocking) {
    console.error(`${finding.severity} ${finding.id} in ${finding.package}: ${finding.title} ${finding.url}`.trim())
    if (finding.reason) console.error(`It fails because ${finding.reason}.`)
  }
  if (blocking.length > 0) {
    console.error(`Fix these advisories or add a reviewed exception to ${path.relative(root, exceptionsPath)}.`)
    process.exitCode = 1
    return
  }
  console.log('No unaccepted npm advisory at high or above.')
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error.message)
    process.exitCode = 1
  })
}
