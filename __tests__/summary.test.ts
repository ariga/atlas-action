import * as fs from 'fs/promises'
import path from 'path'
import { summary } from '@actions/core'
import { Summary } from '../src/atlas'
import { summarize } from '../src/github'
import { expect } from '@jest/globals'

describe('summary', () => {
  const dir = path.join('__tests__', 'testdata', 'summary')
  let summaryFile: string
  beforeAll(async () => {
    if (!process.env.GITHUB_STEP_SUMMARY) {
      summaryFile = path.join(dir, 'summary.txt')
      let f = await fs.open(summaryFile, 'w')
      await f.close()
      process.env.GITHUB_STEP_SUMMARY = summaryFile
    }
  })
  afterAll(async () => {
    if (summaryFile) {
      await fs.rm(summaryFile)
    }
  })
  afterEach(async () => {
    await summary.clear()
  })

  test('base', async () => {
    const f = await fs.readFile(path.join(dir, 'base.txt'))
    const sum: Summary = JSON.parse(f.toString())
    summarize(sum)
    const s = summary.stringify()
    const expected = await fs.readFile(path.join(dir, 'base.expected.txt'))
    expect(s).toEqual(expected.toString())
  })

  test('err', async () => {
    const f = await fs.readFile(path.join(dir, 'error.txt'))
    const sum: Summary = JSON.parse(f.toString())
    summarize(sum)
    const s = summary.stringify()
    const expected = await fs.readFile(path.join(dir, 'error.expected.txt'))
    expect(s).toEqual(expected.toString())
  })
})
