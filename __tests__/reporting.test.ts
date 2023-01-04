import * as fs from 'fs/promises'
import path from 'path'
import * as core from '@actions/core'
import { getInput, summary } from '@actions/core'
import { Summary } from '../src/atlas'
import { report, summarize } from '../src/github'
import { expect } from '@jest/globals'
import { Options, OptionsFromEnv } from '../src/input'
import { CreateReportPayload, Mutation } from '../types/types'
import { CloudReport } from '../src/cloud'

const dir = path.join('__tests__', 'testdata', 'runs')

describe('summary', () => {
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

  const testcase = (name: string, report?: CloudReport) => {
    return async () => {
      const sum = await loadRun(name)
      summarize(sum, report)
      const s = summary.stringify()
      const expected = await fs.readFile(path.join(dir, `${name}.expected.txt`))
      expect(s).toEqual(expected.toString())
    }
  }

  const reports = [
    {
      text: 'Cloud reports',
      diagnostics: [
        {
          text: 'diag1',
          code: 'code1',
          pos: 0
        },
        {
          text: 'diag2',
          code: 'code2',
          pos: 0
        }
      ]
    }
  ]
  test('base', testcase('base'))
  test('err', testcase('error'))
  test('sqlerr', testcase('sqlerr'))
  test('checksum', testcase('checksum'))
  test(
    'cloudURL',
    testcase('cloudurl', {
      result: {
        createReport: {
          url: 'https://tenant.ariga.cloud/ci/123'
        } as CreateReportPayload
      } as Mutation
    } as CloudReport)
  )
  test(
    'cloudReports',
    testcase('cloudreports', {
      result: {
        createReport: {
          url: 'https://tenant.ariga.cloud/ci/123',
          cloudReports: reports
        } as CreateReportPayload
      } as Mutation
    } as CloudReport)
  )
})

describe('annotations', () => {
  let spyErr: jest.SpyInstance
  let origDir: string = getInput('dir')

  beforeAll(() => {
    spyErr = jest.spyOn(core, 'error')
    if (!origDir) {
      process.env.INPUT_DIR = 'migrations/'
    }
  })
  afterAll(() => {
    spyErr.mockReset()
    process.env.INPUT_DIR = origDir
  })

  test('destructive', async () => {
    const sum = await loadRun('destructive')
    let opts: Options = OptionsFromEnv(process.env)
    report(opts, sum)
    expect(spyErr).toHaveBeenCalledWith(
      `Dropping table "orders" (DS102)

Details: https://atlasgo.io/lint/analyzers#DS102`,
      {
        startLine: 2,
        file: 'migrations/20220905074317.up.sql',
        title: 'destructive change detected'
      }
    )
  })
})

async function loadRun(name: string): Promise<Summary> {
  const f = await fs.readFile(path.join(dir, `${name}.txt`))
  return JSON.parse(f.toString())
}
