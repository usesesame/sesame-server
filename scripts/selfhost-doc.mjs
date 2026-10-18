import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)))

export function readSelfHostingDocument() {
  return readFileSync(join(repositoryRoot, 'SELF-HOSTING.md'), 'utf8')
}

export function sectionBlocks(markdown, heading) {
  const lines = markdown.split('\n')
  const level = heading.match(/^#+/)[0].length
  const start = lines.findIndex((line) => line === heading)
  assert.ok(start >= 0, `SELF-HOSTING.md has no heading ${heading}`)
  let end = lines.length
  for (let index = start + 1; index < lines.length; index += 1) {
    const found = lines[index].match(/^(#+) /)
    if (found && found[1].length <= level) {
      end = index
      break
    }
  }
  const blocks = []
  let current = null
  for (const line of lines.slice(start + 1, end)) {
    const fence = line.match(/^\s*(`{3,}|~{3,})(\w*)\s*$/)
    if (current === null) {
      if (fence) current = { marker: fence[1], language: fence[2], lines: [] }
    } else if (fence && fence[2] === '' && fence[1][0] === current.marker[0] && fence[1].length >= current.marker.length) {
      if (current.language === 'sh') blocks.push(dedent(current.lines))
      current = null
    } else {
      current.lines.push(line)
    }
  }
  assert.equal(current, null, `a code block under ${heading} is never closed`)
  return blocks
}

function dedent(lines) {
  const indent = Math.min(...lines.filter((line) => line.trim()).map((line) => line.match(/^ */)[0].length))
  return lines.map((line) => line.slice(indent)).join('\n').trim()
}

export function replaceOnce(text, search, replacement, label) {
  assert.equal(text.split(search).length - 1, 1, `${label} must appear exactly once in the block under test`)
  return text.replace(search, () => replacement)
}
