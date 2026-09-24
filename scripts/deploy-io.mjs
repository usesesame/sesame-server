import { chmod, mkdir, readFile, rename, stat, unlink, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'

// Every file the deploy tool writes carries secrets or state that decides what
// runs next: the database dump, the pinned env, and the deployment records.
// Creating them 0600 and their leaf directory 0700 is the default here, not a
// per-call decision a future edit can forget.
export const fileIO = {
  readText: (path) => readFile(path, 'utf8'),
  writeText: async (path, text, mode = 0o600) => {
    await mkdir(dirname(path), { recursive: true, mode: 0o700 })
    await writeFile(path, text, { mode })
    await chmod(path, mode)
  },
  writeBinary: async (path, bytes, mode = 0o600) => {
    await mkdir(dirname(path), { recursive: true, mode: 0o700 })
    await writeFile(path, bytes, { mode })
    await chmod(path, mode)
    await chmod(dirname(path), 0o700)
  },
  writeJSONAtomic: async (path, value, mode = 0o600) => {
    const staging = `${path}.staging-${process.pid}`
    await mkdir(dirname(path), { recursive: true, mode: 0o700 })
    await writeFile(staging, `${JSON.stringify(value, null, 2)}\n`, { mode })
    await chmod(staging, mode)
    await rename(staging, path)
  },
  renamePath: (from, to) => rename(from, to),
  exists: async (path) => { try { await stat(path); return true } catch { return false } },
  mkdirp: (path) => mkdir(path, { recursive: true }),
  unlink: async (path) => { try { await unlink(path) } catch (error) { if (error.code !== 'ENOENT') throw error } },
}
