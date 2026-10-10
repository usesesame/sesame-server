const ECC_CODEWORDS_PER_BLOCK = [-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28]
const ERROR_CORRECTION_BLOCKS = [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49]
const MEDIUM_FORMAT_BITS = 0
const MIN_VERSION = 1
const MAX_VERSION = 40
const PENALTY_ADJACENT = 3
const PENALTY_BLOCK = 3
const PENALTY_FINDER = 40
const PENALTY_BALANCE = 10

export type QrMatrix = boolean[][]

function rawDataModules(version: number): number {
  let result = (16 * version + 128) * version + 64
  if (version >= 2) {
    const alignCount = Math.floor(version / 7) + 2
    result -= (25 * alignCount - 10) * alignCount - 55
    if (version >= 7) result -= 36
  }
  return result
}

function dataCodewords(version: number): number {
  return Math.floor(rawDataModules(version) / 8) - ECC_CODEWORDS_PER_BLOCK[version] * ERROR_CORRECTION_BLOCKS[version]
}

function alignmentPositions(version: number, size: number): number[] {
  if (version === 1) return []
  const count = Math.floor(version / 7) + 2
  const step = version === 32 ? 26 : Math.ceil((version * 4 + 4) / (count * 2 - 2)) * 2
  const result = [6]
  for (let position = size - 7; result.length < count; position -= step) result.splice(1, 0, position)
  return result
}

function multiply(x: number, y: number): number {
  let z = 0
  for (let i = 7; i >= 0; i -= 1) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d)
    z ^= ((y >>> i) & 1) * x
  }
  return z
}

function divisor(degree: number): number[] {
  const result = new Array<number>(degree).fill(0)
  result[degree - 1] = 1
  let root = 1
  for (let i = 0; i < degree; i += 1) {
    for (let j = 0; j < result.length; j += 1) {
      result[j] = multiply(result[j], root)
      if (j + 1 < result.length) result[j] ^= result[j + 1]
    }
    root = multiply(root, 0x02)
  }
  return result
}

function remainder(data: number[], generator: number[]): number[] {
  const result = new Array<number>(generator.length).fill(0)
  for (const byte of data) {
    const factor = byte ^ (result.shift() as number)
    result.push(0)
    generator.forEach((coefficient, index) => { result[index] ^= multiply(coefficient, factor) })
  }
  return result
}

function bitAt(value: number, index: number): boolean {
  return ((value >>> index) & 1) !== 0
}

function encodeData(bytes: Uint8Array, version: number): number[] {
  const bits: number[] = []
  const push = (value: number, count: number) => {
    for (let i = count - 1; i >= 0; i -= 1) bits.push((value >>> i) & 1)
  }
  push(0x4, 4)
  push(bytes.length, version <= 9 ? 8 : 16)
  for (const byte of bytes) push(byte, 8)
  const capacityBits = dataCodewords(version) * 8
  push(0, Math.min(4, capacityBits - bits.length))
  push(0, (8 - (bits.length % 8)) % 8)
  for (let pad = 0xec; bits.length < capacityBits; pad ^= 0xec ^ 0x11) push(pad, 8)
  const codewords = new Array<number>(bits.length / 8).fill(0)
  bits.forEach((bit, index) => { codewords[index >>> 3] |= bit << (7 - (index & 7)) })
  return codewords
}

function interleave(data: number[], version: number): number[] {
  const blocks = ERROR_CORRECTION_BLOCKS[version]
  const eccLength = ECC_CODEWORDS_PER_BLOCK[version]
  const raw = Math.floor(rawDataModules(version) / 8)
  const shortBlocks = blocks - (raw % blocks)
  const shortLength = Math.floor(raw / blocks)
  const generator = divisor(eccLength)
  const assembled: number[][] = []
  for (let i = 0, offset = 0; i < blocks; i += 1) {
    const dataLength = shortLength - eccLength + (i < shortBlocks ? 0 : 1)
    const block = data.slice(offset, offset + dataLength)
    offset += dataLength
    const ecc = remainder(block, generator)
    if (i < shortBlocks) block.push(0)
    assembled.push(block.concat(ecc))
  }
  const result: number[] = []
  for (let i = 0; i < assembled[0].length; i += 1) {
    assembled.forEach((block, j) => {
      if (i !== shortLength - eccLength || j >= shortBlocks) result.push(block[i])
    })
  }
  return result
}

class Builder {
  readonly version: number
  readonly size: number
  readonly modules: boolean[][]
  readonly reserved: boolean[][]

  constructor(version: number) {
    this.version = version
    this.size = version * 4 + 17
    this.modules = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
    this.reserved = Array.from({ length: this.size }, () => new Array<boolean>(this.size).fill(false))
    this.drawFunctionPatterns()
  }

  private set(x: number, y: number, dark: boolean) {
    this.modules[y][x] = dark
    this.reserved[y][x] = true
  }

  private drawFunctionPatterns() {
    for (let i = 0; i < this.size; i += 1) {
      this.set(6, i, i % 2 === 0)
      this.set(i, 6, i % 2 === 0)
    }
    this.drawFinder(3, 3)
    this.drawFinder(this.size - 4, 3)
    this.drawFinder(3, this.size - 4)
    const positions = alignmentPositions(this.version, this.size)
    positions.forEach((cy, i) => positions.forEach((cx, j) => {
      const overlapsFinder = (i === 0 && j === 0) || (i === 0 && j === positions.length - 1) || (i === positions.length - 1 && j === 0)
      if (!overlapsFinder) this.drawAlignment(cx, cy)
    }))
    this.drawFormat(0)
    this.drawVersion()
  }

  private drawFinder(cx: number, cy: number) {
    for (let dy = -4; dy <= 4; dy += 1) {
      for (let dx = -4; dx <= 4; dx += 1) {
        const distance = Math.max(Math.abs(dx), Math.abs(dy))
        const x = cx + dx
        const y = cy + dy
        if (x >= 0 && x < this.size && y >= 0 && y < this.size) this.set(x, y, distance !== 2 && distance !== 4)
      }
    }
  }

  private drawAlignment(cx: number, cy: number) {
    for (let dy = -2; dy <= 2; dy += 1) {
      for (let dx = -2; dx <= 2; dx += 1) this.set(cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1)
    }
  }

  drawFormat(mask: number) {
    const data = (MEDIUM_FORMAT_BITS << 3) | mask
    let rem = data
    for (let i = 0; i < 10; i += 1) rem = (rem << 1) ^ ((rem >>> 9) * 0x537)
    const bits = ((data << 10) | rem) ^ 0x5412
    for (let i = 0; i <= 5; i += 1) this.set(8, i, bitAt(bits, i))
    this.set(8, 7, bitAt(bits, 6))
    this.set(8, 8, bitAt(bits, 7))
    this.set(7, 8, bitAt(bits, 8))
    for (let i = 9; i < 15; i += 1) this.set(14 - i, 8, bitAt(bits, i))
    for (let i = 0; i < 8; i += 1) this.set(this.size - 1 - i, 8, bitAt(bits, i))
    for (let i = 8; i < 15; i += 1) this.set(8, this.size - 15 + i, bitAt(bits, i))
    this.set(8, this.size - 8, true)
  }

  private drawVersion() {
    if (this.version < 7) return
    let rem = this.version
    for (let i = 0; i < 12; i += 1) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25)
    const bits = (this.version << 12) | rem
    for (let i = 0; i < 18; i += 1) {
      const dark = bitAt(bits, i)
      const a = this.size - 11 + (i % 3)
      const b = Math.floor(i / 3)
      this.set(a, b, dark)
      this.set(b, a, dark)
    }
  }

  placeData(codewords: number[]) {
    let index = 0
    for (let right = this.size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5
      for (let vertical = 0; vertical < this.size; vertical += 1) {
        for (let j = 0; j < 2; j += 1) {
          const x = right - j
          const upward = ((right + 1) & 2) === 0
          const y = upward ? this.size - 1 - vertical : vertical
          if (!this.reserved[y][x] && index < codewords.length * 8) {
            this.modules[y][x] = bitAt(codewords[index >>> 3], 7 - (index & 7))
            index += 1
          }
        }
      }
    }
  }

  applyMask(mask: number) {
    for (let y = 0; y < this.size; y += 1) {
      for (let x = 0; x < this.size; x += 1) {
        let invert: boolean
        switch (mask) {
          case 0: invert = (x + y) % 2 === 0; break
          case 1: invert = y % 2 === 0; break
          case 2: invert = x % 3 === 0; break
          case 3: invert = (x + y) % 3 === 0; break
          case 4: invert = (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0; break
          case 5: invert = ((x * y) % 2) + ((x * y) % 3) === 0; break
          case 6: invert = ((((x * y) % 2) + ((x * y) % 3)) % 2) === 0; break
          default: invert = ((((x + y) % 2) + ((x * y) % 3)) % 2) === 0; break
        }
        if (!this.reserved[y][x] && invert) this.modules[y][x] = !this.modules[y][x]
      }
    }
  }

  private finderCount(history: number[]): number {
    const n = history[1]
    const core = n > 0 && history[2] === n && history[3] === n * 3 && history[4] === n && history[5] === n
    return (core && history[0] >= n * 4 && history[6] >= n ? 1 : 0) + (core && history[6] >= n * 4 && history[0] >= n ? 1 : 0)
  }

  private addHistory(run: number, history: number[]) {
    if (history[0] === 0) run += this.size
    history.pop()
    history.unshift(run)
  }

  private terminate(color: boolean, run: number, history: number[]): number {
    if (color) {
      this.addHistory(run, history)
      run = 0
    }
    run += this.size
    this.addHistory(run, history)
    return this.finderCount(history)
  }

  private linePenalty(read: (a: number, b: number) => boolean): number {
    let result = 0
    for (let a = 0; a < this.size; a += 1) {
      let color = false
      let run = 0
      const history = [0, 0, 0, 0, 0, 0, 0]
      for (let b = 0; b < this.size; b += 1) {
        if (read(a, b) === color) {
          run += 1
          if (run === 5) result += PENALTY_ADJACENT
          else if (run > 5) result += 1
        } else {
          this.addHistory(run, history)
          if (!color) result += this.finderCount(history) * PENALTY_FINDER
          color = read(a, b)
          run = 1
        }
      }
      result += this.terminate(color, run, history) * PENALTY_FINDER
    }
    return result
  }

  penalty(): number {
    let result = this.linePenalty((y, x) => this.modules[y][x]) + this.linePenalty((x, y) => this.modules[y][x])
    for (let y = 0; y < this.size - 1; y += 1) {
      for (let x = 0; x < this.size - 1; x += 1) {
        const color = this.modules[y][x]
        if (color === this.modules[y][x + 1] && color === this.modules[y + 1][x] && color === this.modules[y + 1][x + 1]) result += PENALTY_BLOCK
      }
    }
    let dark = 0
    for (const row of this.modules) for (const cell of row) if (cell) dark += 1
    const total = this.size * this.size
    result += (Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1) * PENALTY_BALANCE
    return result
  }
}

export function encodeQr(text: string): QrMatrix {
  const bytes = new TextEncoder().encode(text)
  let version = MIN_VERSION
  for (;; version += 1) {
    if (version > MAX_VERSION) throw new RangeError('The text is too long for a QR code.')
    const countBits = version <= 9 ? 8 : 16
    if (bytes.length < 1 << countBits && 4 + countBits + bytes.length * 8 <= dataCodewords(version) * 8) break
  }
  const codewords = interleave(encodeData(bytes, version), version)
  const builder = new Builder(version)
  builder.placeData(codewords)
  let best = 0
  let bestPenalty = Infinity
  for (let mask = 0; mask < 8; mask += 1) {
    builder.applyMask(mask)
    builder.drawFormat(mask)
    const penalty = builder.penalty()
    if (penalty < bestPenalty) {
      best = mask
      bestPenalty = penalty
    }
    builder.applyMask(mask)
  }
  builder.applyMask(best)
  builder.drawFormat(best)
  return builder.modules
}

export const QUIET_ZONE = 4

export function qrPath(matrix: QrMatrix): string {
  const parts: string[] = []
  matrix.forEach((row, y) => {
    let x = 0
    while (x < row.length) {
      if (!row[x]) {
        x += 1
        continue
      }
      const start = x
      while (x < row.length && row[x]) x += 1
      parts.push(`M${start + QUIET_ZONE} ${y + QUIET_ZONE}h${x - start}v1h${start - x}z`)
    }
  })
  return parts.join('')
}

export function qrViewSize(matrix: QrMatrix): number {
  return matrix.length + QUIET_ZONE * 2
}
