import assert from 'node:assert/strict'
import { readdirSync } from 'node:fs'
import { join } from 'node:path'

const identifier = /^[0-9a-f]{32}$/
const migrationName = /^0*(\d+)_[a-z0-9_]+\.sql$/

export function parseRecoveryJson(output, label) {
  const text = String(output)
  const start = text.indexOf('{')
  const end = text.lastIndexOf('}')
  assert.ok(start >= 0 && end > start, `${label} printed no JSON object`)
  let value
  try {
    value = JSON.parse(text.slice(start, end + 1))
  } catch {
    assert.fail(`${label} printed malformed JSON`)
  }
  assert.ok(value && typeof value === 'object' && !Array.isArray(value), `${label} printed JSON that is not an object`)
  return value
}

export function assertRecoveryStatus(value, label) {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value), `${label} is not an object`)
  assert.equal(typeof value.pending, 'boolean', `${label} has no pending boolean`)
  assert.equal(typeof value.generation, 'string', `${label} has no generation string`)
  assertTime(value.startedAt, `${label} startedAt`, true)
  assertTime(value.completedAt, `${label} completedAt`, true)
  assert.ok(Array.isArray(value.owners), `${label} has no owners array`)
  assert.ok(Array.isArray(value.devices), `${label} has no devices array`)
  if (value.pending) {
    assert.match(value.generation, identifier, `${label} has no valid pending generation`)
    assert.ok(value.startedAt, `${label} has no start time while recovery is pending`)
    assert.equal(value.completedAt, null, `${label} has a completion time while recovery is pending`)
  } else if (value.generation === '') {
    assert.equal(value.startedAt, null, `${label} has a start time without a generation`)
    assert.equal(value.completedAt, null, `${label} has a completion time without a generation`)
  } else {
    assert.match(value.generation, identifier, `${label} has a malformed generation`)
    assert.ok(value.startedAt, `${label} has a generation without a start time`)
    assert.ok(value.completedAt, `${label} has no completion time for a completed generation`)
  }
  const ownerIDs = new Set()
  for (const owner of value.owners) {
    assert.ok(owner && typeof owner === 'object' && !Array.isArray(owner), `${label} has a malformed owner`)
    assert.match(owner.id, identifier, `${label} has an owner without a valid id`)
    assert.equal(typeof owner.name, 'string', `${label} has an owner without a name`)
    assert.equal(typeof owner.setupPending, 'boolean', `${label} has an owner without setupPending`)
    assertTime(owner.lastLoginAt, `${label} owner lastLoginAt`, true)
    assert.ok(!ownerIDs.has(owner.id), `${label} lists an owner twice`)
    ownerIDs.add(owner.id)
  }
  const deviceIDs = new Set()
  for (const device of value.devices) {
    assert.ok(device && typeof device === 'object' && !Array.isArray(device), `${label} has a malformed device`)
    assert.match(device.id, identifier, `${label} has a device without a valid id`)
    assert.equal(typeof device.name, 'string', `${label} has a device without a name`)
    assert.ok(device.holder && typeof device.holder === 'object' && !Array.isArray(device.holder), `${label} has a device without a holder`)
    assert.ok(['owner', 'member'].includes(device.holder.kind), `${label} has a device with an unknown holder kind`)
    assert.match(device.holder.id, identifier, `${label} has a device holder without a valid id`)
    assert.equal(typeof device.holder.name, 'string', `${label} has a device holder without a name`)
    assertTime(device.expiresAt, `${label} device expiresAt`, false)
    assertTime(device.lastSeenAt, `${label} device lastSeenAt`, false)
    assertTime(device.revokedAt, `${label} device revokedAt`, true)
    assert.ok(!deviceIDs.has(device.id), `${label} lists a device twice`)
    deviceIDs.add(device.id)
  }
  return value
}

export function approvalSelection(status, { ownerIds = [], deviceIds = [] } = {}) {
  assertRecoveryStatus(status, 'recovery status for approval')
  assert.equal(status.pending, true, 'there is no pending recovery to approve')
  assert.ok(Array.isArray(ownerIds) && Array.isArray(deviceIds), 'the recovery selection must hold owner and device arrays')
  const owners = new Map(status.owners.map((owner) => [owner.id, owner]))
  const devices = new Map(status.devices.map((device) => [device.id, device]))
  assert.equal(new Set(ownerIds).size, ownerIds.length, 'an owner ID is selected twice')
  assert.equal(new Set(deviceIds).size, deviceIds.length, 'a device ID is selected twice')
  for (const id of ownerIds) {
    const owner = owners.get(id)
    assert.ok(owner, `the owner ${id} is not part of the pending recovery`)
    assert.equal(owner.setupPending, false, `the owner ${id} has not completed setup`)
  }
  const completeOwners = status.owners.filter((owner) => !owner.setupPending)
  if (completeOwners.length > 0) assert.ok(ownerIds.length > 0, 'at least one complete owner must be retained')
  const now = Date.now()
  for (const id of deviceIds) {
    const device = devices.get(id)
    assert.ok(device, `the device ${id} is not part of the pending recovery`)
    assert.equal(device.revokedAt, null, `the device ${id} is already revoked`)
    assert.ok(Date.parse(device.expiresAt) > now, `the device ${id} has expired`)
    if (device.holder.kind === 'owner') assert.ok(ownerIds.includes(device.holder.id), `the device ${id} belongs to an owner that is not retained`)
  }
  return { ownerIds: [...ownerIds], deviceIds: [...deviceIds] }
}

export function approvalArguments(status, selection) {
  const chosen = approvalSelection(status, selection)
  const args = ['recover', '--confirm', status.generation]
  for (const id of chosen.ownerIds) args.push('--retain-owner', id)
  for (const id of chosen.deviceIds) args.push('--retain-device', id)
  args.push('--json')
  return args
}

export function approvalEnvironment(status, selection) {
  const chosen = approvalSelection(status, selection)
  assert.ok(chosen.ownerIds.length <= 1, 'the documented approval block holds one owner placeholder')
  assert.ok(chosen.deviceIds.length <= 1, 'the documented approval block holds one device placeholder')
  const environment = { SESAME_RECOVERY_GENERATION: status.generation }
  if (chosen.ownerIds.length === 1) environment.SESAME_RETAIN_OWNER_ID = chosen.ownerIds[0]
  if (chosen.deviceIds.length === 1) environment.SESAME_RETAIN_DEVICE_ID = chosen.deviceIds[0]
  return environment
}

export function assertApprovalResult(value, { status, ownerIds = [], deviceIds = [] } = {}) {
  assert.ok(value && typeof value === 'object' && !Array.isArray(value), 'the approval result is not an object')
  assert.equal(value.pending, false, 'the approval result still reports a pending recovery')
  assert.equal(typeof value.generation, 'string', 'the approval result has no generation')
  assert.equal(typeof value.alreadyComplete, 'boolean', 'the approval result has no alreadyComplete boolean')
  for (const field of ['retainedOwners', 'removedOwners', 'retainedDevices', 'revokedDevices']) {
    assert.ok(Number.isInteger(value[field]) && value[field] >= 0, `the approval result has no valid ${field}`)
  }
  if (status) {
    assert.match(status.generation, identifier, 'the approval status has no valid generation')
    assert.equal(value.generation, status.generation, 'the approval result names another generation')
  }
  if (status && !value.alreadyComplete) {
    assert.deepEqual([...ownerIds].sort(), [...new Set(ownerIds)].sort(), 'an owner ID is selected twice')
    assert.deepEqual([...deviceIds].sort(), [...new Set(deviceIds)].sort(), 'a device ID is selected twice')
    const active = status.devices.filter((device) => device.revokedAt === null).length
    assert.equal(value.retainedOwners, ownerIds.length, 'the approval result retained another number of owners')
    assert.equal(value.removedOwners, status.owners.length - ownerIds.length, 'the approval result removed another number of owners')
    assert.equal(value.retainedDevices, deviceIds.length, 'the approval result retained another number of devices')
    assert.equal(value.revokedDevices, active - deviceIds.length, 'the approval result revoked another number of devices')
  }
  return value
}

export function schemaFromMigrationNames(names) {
  const versions = []
  for (const name of names) {
    const match = name.match(migrationName)
    if (match) versions.push(Number(match[1]))
  }
  assert.ok(versions.length > 0, 'the checkout holds no database migrations')
  assert.equal(new Set(versions).size, versions.length, 'the database migrations use one number twice')
  return Math.max(...versions)
}

export function readInstalledSchema(root) {
  return schemaFromMigrationNames(readdirSync(join(root, 'internal', 'selfhost', 'sqlitestore', 'migrations')))
}

export function skipAllowedWithoutUpgrade(schema) {
  return schema <= 1
}

export function redactSensitiveLog(text) {
  return String(text)
    .replace(/(setup#token=)[A-Za-z0-9_-]+/g, '$1[redacted]')
    .replace(/((?:^|[?&#\s])code=)[A-Za-z0-9_-]+/g, '$1[redacted]')
}

function assertTime(value, label, nullable) {
  if (value === null) {
    assert.ok(nullable, `${label} is missing`)
    return
  }
  assert.equal(typeof value, 'string', `${label} is not a time string`)
  assert.ok(!Number.isNaN(Date.parse(value)), `${label} is not a valid time`)
}
