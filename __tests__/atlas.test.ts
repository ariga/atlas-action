import { run } from '../src/main'
import { expect } from '@jest/globals'
import { tmpdir } from 'os'
import { getExecOutput } from '@actions/exec'
import * as path from 'path'
import { SimpleGit, simpleGit } from 'simple-git'
import { copyFile, mkdir, mkdtemp, readdir, rm, stat } from 'fs/promises'
import { AtlasResult, ExitCodes, installAtlas } from '../src/atlas'
import * as core from '@actions/core'
import * as github from '@actions/github'
import nock from 'nock'
import * as http from '@actions/http-client'

jest.setTimeout(30000)
const originalENV = { ...process.env }

describe('install', () => {
  let base: string
  beforeEach(async () => {
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        // The path to a temporary directory on the runner (must be defined)
        RUNNER_TEMP: base,
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    }
  })
  afterEach(() => {
    rm(base, { recursive: true })
    process.env = { ...originalENV }
  })

  test('install the latest version of atlas', async () => {
    const bin = await installAtlas('latest')
    await expect(stat(bin)).resolves.toBeTruthy()
  })

  test('installs specific version of atlas', async () => {
    const expectedVersion = 'v0.4.2'
    const bin = await installAtlas(expectedVersion)
    await expect(stat(bin)).resolves.toBeTruthy()
    const output = await getExecOutput(`${bin}`, ['version'])
    expect(output.stdout.includes(expectedVersion)).toBeTruthy()
  })
})

describe('run with latest', () => {
  let base: string

  beforeEach(async () => {
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        RUNNER_TEMP: base,
        'INPUT_DEV-DB': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        INPUT_LATEST: '1',
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    }
  })

  afterEach(async () => {
    await rm(base, { recursive: true })
    process.env = { ...originalENV }
  })

  test('successful no issues', async () => {
    const migrationsDir = path.join(base, 'migrations')
    await mkdir(migrationsDir)
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE_NAME>.
    process.env.INPUT_DIR = migrationsDir
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(res.raw).toBe('[]')
    expect(res.fileReports).toEqual([])
  })

  test('successful with wrong sum file', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE_NAME>.
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-wrong-sum'
    )
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Failure)
    expect(res.fileReports).toHaveLength(1)
    expect(res.fileReports?.[0].Name).toEqual('atlas.sum')
    expect(res.fileReports?.[0].Error).toEqual('checksum mismatch')
    expect(res.raw).toEqual(
      '[{"Name":"atlas.sum","Error":"checksum mismatch"}]'
    )
  })

  test('successful with diagnostics', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE_NAME>.
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )

    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(res.fileReports).toHaveLength(1)
    expect(res.fileReports?.[0].Name).toBe('20220619130911_second.sql')
    expect(res.fileReports?.[0].Text).toBe(
      '-- DROP "tbl2" table\nDROP TABLE tbl2;\n'
    )
    expect(res.fileReports?.[0].Reports).toHaveLength(1)
    expect(res.fileReports?.[0].Reports?.[0].Text).toBe(
      'Destructive changes detected in file 20220619130911_second.sql'
    )
    expect(res.fileReports?.[0].Reports?.[0].Diagnostics).toHaveLength(1)
    expect(res.fileReports?.[0].Reports?.[0].Diagnostics?.[0].Text).toBe(
      'Dropping table "tbl2"'
    )
    expect(res.fileReports?.[0].Reports?.[0].Diagnostics?.[0].Pos).toBe(21)
    const expectedRaw = [
      {
        Name: '20220619130911_second.sql',
        Text: '-- DROP "tbl2" table\nDROP TABLE tbl2;\n',
        Reports: [
          {
            Text: 'Destructive changes detected in file 20220619130911_second.sql',
            Diagnostics: [{ Pos: 21, Text: 'Dropping table "tbl2"' }]
          }
        ]
      }
    ]
    expect(res.raw).toBe(JSON.stringify(expectedRaw))
  })

  test('successful run with golang migrate format', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE_NAME>.
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    process.env['INPUT_DIR-FORMAT'] = 'golang-migrate'
    process.env['INPUT_LATEST'] = '4'
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Success)
    expect(res.fileReports).toHaveLength(2)
    expect(res.fileReports?.[0].Name).toEqual('1_initial.up.sql')
    expect(res.fileReports?.[0].Text).toContain('CREATE TABLE tbl')
    expect(res.fileReports?.[0].Error).toBeFalsy()
    expect(res.fileReports?.[1].Name).toEqual('2_second_migration.up.sql')
    expect(res.fileReports?.[1].Text).toContain('CREATE TABLE tbl_2')
    expect(res.fileReports?.[1].Error).toBeFalsy()
  })

  test('fail on wrong migration dir format', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE_NAME>.
    process.env['INPUT_DIR-FORMAT'] = 'incorrect'
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Failure)
    expect(res.raw).toEqual(
      '[{"Name":"1_initial.down.sql","Error":"executing statement: \\"DROP TABLE tbl;\\": no such table: tbl"}]'
    )
  })
})

describe('run with git base', () => {
  let gitRepo: string
  let base: string

  beforeEach(async () => {
    const changesBranch = 'changes'
    const baseBranch = 'master'
    gitRepo = await mkdtemp(`${tmpdir()}${path.sep}`)
    const migrationsDir = path.join(gitRepo, 'migrations')
    await mkdir(migrationsDir)
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        RUNNER_TEMP: base,
        GITHUB_BASE_REF: baseBranch,
        'INPUT_DEV-DB': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        INPUT_LATEST: '0',
        GITHUB_WORKSPACE: gitRepo,
        INPUT_DIR: migrationsDir,
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    }
    const git: SimpleGit = simpleGit(gitRepo, {
      config: [
        'user.name=zeevmoney',
        'user.email=zeev@ariga.io',
        `init.defaultBranch=${baseBranch}`
      ]
    })
    await git.init()
    const baseBranchFiles = path.join(
      '__tests__',
      'testdata',
      'git-repo',
      'base'
    )
    for (const file of await readdir(baseBranchFiles)) {
      await copyFile(
        path.join(baseBranchFiles, file),
        path.join(migrationsDir, file)
      )
    }
    await git.add('.').commit('Initial commit')
    await git.checkoutLocalBranch(changesBranch)
    const changesPath = path.join(
      '__tests__',
      'testdata',
      'git-repo',
      'changes'
    )
    for (const file of await readdir(changesPath)) {
      await copyFile(
        path.join(changesPath, file),
        path.join(migrationsDir, file)
      )
    }
    await git.add('.').commit('changes')
  })

  afterEach(async () => {
    await rm(gitRepo, { recursive: true })
    await rm(base, { recursive: true })
    process.env = { ...originalENV }
  })

  test('successful', async () => {
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(res.fileReports).toHaveLength(1)
    expect(res.fileReports?.[0].Name).toBe('20220728131023.sql')
    expect(res.fileReports?.[0].Text).toBe(
      `-- DROP "tbl" table\nDROP TABLE tbl;\n`
    )
    expect(res.fileReports?.[0].Reports).toHaveLength(1)
    expect(res.fileReports?.[0].Reports?.[0].Text).toBe(
      'Destructive changes detected in file 20220728131023.sql'
    )
    expect(res.fileReports?.[0].Reports?.[0].Diagnostics?.[0].Text).toBe(
      `Dropping table "tbl"`
    )
    const expectedRaw = [
      {
        Name: '20220728131023.sql',
        Text: '-- DROP "tbl" table\nDROP TABLE tbl;\n',
        Reports: [
          {
            Text: 'Destructive changes detected in file 20220728131023.sql',
            Diagnostics: [{ Pos: 20, Text: 'Dropping table "tbl"' }]
          }
        ]
      }
    ]
    expect(res.raw).toBe(JSON.stringify(expectedRaw))
  })
})

describe('report to GitHub', () => {
  let base: string
  let spyOnNotice: jest.SpyInstance,
    spyOnError: jest.SpyInstance,
    spyOnSetFailed: jest.SpyInstance

  beforeEach(async () => {
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        RUNNER_TEMP: base,
        INPUT_LATEST: '1',
        'INPUT_DEV-DB': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    }
    spyOnNotice = jest.spyOn(core, 'notice')
    spyOnError = jest.spyOn(core, 'error')
    spyOnSetFailed = jest.spyOn(core, 'setFailed')
  })

  afterEach(async () => {
    process.env = { ...originalENV }
    await rm(base, { recursive: true })
    spyOnNotice.mockReset()
    spyOnError.mockReset()
    spyOnSetFailed.mockReset()
  })

  test('regular', async () => {
    const spyOnNotice = jest.spyOn(core, 'notice')
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(spyOnNotice).toHaveBeenCalledTimes(1)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnNotice).toHaveBeenCalledWith(
      'Destructive changes detected in file 20220619130911_second.sql: Dropping table "tbl2"',
      {
        file: '20220619130911_second.sql',
        startLine: 0,
        title: 'Destructive changes detected in file 20220619130911_second.sql'
      }
    )
  })

  test('atlas known error', async () => {
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-broken-file'
    )
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Failure)
    expect(spyOnNotice).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(1)
    expect(spyOnError).toHaveBeenCalledWith(
      'executing statement: near "BAD": syntax error',
      {
        file: '20220318104614_initial.sql',
        startLine: 0,
        title: 'Error in Migrations file'
      }
    )
  })

  test('atlas unknown error - no report', async () => {
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'nothing-here')
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Failure)
    expect(spyOnNotice).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
    expect(spyOnSetFailed).toHaveBeenCalledWith(
      'Atlas failed with code 1: Error: sql/migrate: stat __tests__/testdata/nothing-here: no such file or directory\n'
    )
  })
})

describe('all reports', () => {
  const originalContext = { ...github.context }
  let base: string,
    gqlScope: nock.Interceptor,
    spyOnNotice: jest.SpyInstance,
    spyOnError: jest.SpyInstance,
    spyOnWarning: jest.SpyInstance

  beforeEach(async () => {
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        RUNNER_TEMP: base,
        INPUT_LATEST: '1',
        'INPUT_DEV-DB': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        'INPUT_CLOUD-URL': `https://ci.ariga.cloud`,
        'INPUT_ARIGA-TOKEN': `mysecrettoken`,
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    }
    spyOnNotice = jest.spyOn(core, 'notice')
    spyOnError = jest.spyOn(core, 'error')
    spyOnWarning = jest.spyOn(core, 'warning')
    // Mock GitHub Context
    Object.defineProperty(github, 'context', {
      value: {
        eventName: 'pull_request',
        payload: {
          pull_request: {
            html_url: 'https://github.com/ariga/atlasci-action/pull/1'
          }
        }
      }
    })
    const url = process.env['INPUT_CLOUD-URL'] as string
    gqlScope = nock(url)
      .post('/api/query')
      .matchHeader(
        'Authorization',
        `Bearer ${process.env['INPUT_ARIGA-TOKEN']}`
      )
      .matchHeader('User-Agent', process.env.ATLASCI_USER_AGENT as string)
  })

  afterEach(async () => {
    await rm(base, { recursive: true })
    spyOnNotice.mockReset()
    spyOnError.mockReset()
    spyOnWarning.mockReset()
    process.env = { ...originalENV }
    Object.defineProperty(github, 'context', {
      value: originalContext
    })
  })

  test('successfully', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    const scope = gqlScope.reply(http.HttpCodes.OK, {
      data: {
        createReport: {
          runID: '8589934593',
          url: cloudURL
        }
      }
    })
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnNotice).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnNotice).toHaveBeenNthCalledWith(
      1,
      'Destructive changes detected in file 20220619130911_second.sql: Dropping table "tbl2"',
      {
        file: '20220619130911_second.sql',
        startLine: 0,
        title: 'Destructive changes detected in file 20220619130911_second.sql'
      }
    )
    expect(spyOnNotice).toHaveBeenNthCalledWith(
      2,
      'For full report visit: https://ariga.cloud.test/ci-runs/8589934593'
    )
  })
})
