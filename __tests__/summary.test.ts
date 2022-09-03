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

  const testcase = (name: string) => {
    return async () => {
      const f = await fs.readFile(path.join(dir, `${name}.txt`))
      const sum: Summary = JSON.parse(f.toString())
      summarize(sum)
      const s = summary.stringify()
      const expected = await fs.readFile(path.join(dir, `${name}.expected.txt`))
      expect(s).toEqual(expected.toString())
    }
  }

  test('base', testcase('base'))

  test('err', testcase('error'))

  test('checksum', testcase('checksum'))
})
