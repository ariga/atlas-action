import { run } from '../src/main'
import { expect } from '@jest/globals'
import { tmpdir } from 'os'
import { getExecOutput } from '@actions/exec'
import * as path from 'path'
import { SimpleGit, simpleGit } from 'simple-git'
import {
  copyFile,
  mkdir,
  mkdtemp,
  readdir,
  readFile,
  rm,
  stat
} from 'fs/promises'
import * as core from '@actions/core'
import * as github from '@actions/github'
import nock from 'nock'
import * as http from '@actions/http-client'
import { AtlasResult, ExitCodes, installAtlas } from '../src/atlas'
import {
  ARCHITECTURE,
  BASE_ADDRESS,
  getDownloadURL,
  LATEST_RELEASE,
  mutation,
  S3_FOLDER
} from '../src/cloud'
import { Variables } from 'graphql-request/src/types'

jest.mock('../src/cloud', () => {
  const actual = jest.requireActual('../src/cloud')
  return {
    ...actual,
    getDownloadURL: jest.fn(
      (version: string) =>
        new URL(
          `${BASE_ADDRESS}/${S3_FOLDER}/atlas-${ARCHITECTURE}-${version}?test=1`
        )
    )
  }
})

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
    nock.cleanAll()
  })

  test('install the latest version of atlas', async () => {
    const bin = await installAtlas(LATEST_RELEASE)
    await expect(stat(bin)).resolves.toBeTruthy()
  })

  test('installs specific version of atlas', async () => {
    const expectedVersion = 'v0.4.2'
    const bin = await installAtlas(expectedVersion)
    await expect(stat(bin)).resolves.toBeTruthy()
    const output = await getExecOutput(`${bin}`, ['version'])
    expect(output.stdout.includes(expectedVersion)).toBeTruthy()
  })

  test('append test query params', async () => {
    const url = getDownloadURL(LATEST_RELEASE)
    expect(url.toString()).toEqual(
      `https://release.ariga.io/atlas/atlas-${ARCHITECTURE}-latest?test=1`
    )
    const content = 'OK'
    const scope = nock(`${url.protocol}//${url.host}`)
      .get(`${url.pathname}${url.search}`)
      .matchHeader('user-agent', 'actions/tool-cache')
      .reply(200, content)
    const bin = await installAtlas('latest')
    expect(scope.isDone()).toBeTruthy()
    await expect(stat(bin)).resolves.toBeTruthy()
    await expect(readFile(bin)).resolves.toEqual(Buffer.from(content))
  })
})

describe('run with "latest" flag', () => {
  let base: string, spyOnSetFailed: jest.SpyInstance

  beforeEach(async () => {
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
    process.env = {
      ...process.env,
      ...{
        RUNNER_TEMP: base,
        'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        INPUT_LATEST: '1',
        ATLASCI_USER_AGENT: 'test-atlasci-action',
        'INPUT_REPORT-SCHEMA': 'false'
      }
    }
    spyOnSetFailed = jest.spyOn(core, 'setFailed')
  })

  afterEach(async () => {
    await rm(base, { recursive: true })
    process.env = { ...originalENV }
    spyOnSetFailed.mockReset()
  })

  test('successful no issues', async () => {
    const migrationsDir = path.join(base, 'migrations')
    await mkdir(migrationsDir)
    process.env.INPUT_DIR = migrationsDir
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(res.summary?.Files).toBeUndefined()
    expect(res.summary?.Schema).toBeNull()
    const expected = {
      Env: {
        Driver: 'sqlite3',
        URL: {
          Scheme: 'sqlite',
          Opaque: '',
          User: null,
          Host: 'test',
          Path: '',
          RawPath: '',
          ForceQuery: false,
          RawQuery: 'mode=memory&cache=shared&_fk=1',
          Fragment: '',
          RawFragment: '',
          DSN: 'file:test?mode=memory&cache=shared&_fk=1',
          Schema: 'main'
        },
        // This value is random and changes on every run.
        Dir: (res.summary?.Env as { [key: string]: string }).Dir
      },
      Steps: [
        {
          Name: 'Migration Integrity Check',
          Text: 'File atlas.sum is valid'
        },
        {
          Name: 'Detect New Migration Files',
          Text: 'Found 0 new migration files (from 0 total)'
        },
        {
          Name: 'Replay Migration Files',
          Text: 'Loaded 0 changes on dev database'
        }
      ],
      Schema: null
    }
    expect(res.summary).toEqual(expected)
    expect(res.raw).toEqual(JSON.stringify(expected))
  })

  test('fail on wrong sum file', async () => {
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-wrong-sum'
    )
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Failure)
    const expected = {
      Env: {
        Driver: 'sqlite3',
        URL: {
          Scheme: 'sqlite',
          Opaque: '',
          User: null,
          Host: 'test',
          Path: '',
          RawPath: '',
          ForceQuery: false,
          RawQuery: 'mode=memory&cache=shared&_fk=1',
          Fragment: '',
          RawFragment: '',
          DSN: 'file:test?mode=memory&cache=shared&_fk=1',
          Schema: 'main'
        },
        Dir: '__tests__/testdata/sqlite-wrong-sum'
      },
      Steps: [
        {
          Name: 'Migration Integrity Check',
          Text: 'File atlas.sum is invalid',
          Error: 'checksum mismatch'
        }
      ],
      Schema: null,
      Files: [
        {
          Name: 'atlas.sum',
          Error: 'checksum mismatch'
        }
      ]
    }
    expect(res.summary).toEqual(expected)
    expect(res.raw).toEqual(JSON.stringify(expected))
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
  })

  test('successful with diagnostics', async () => {
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )

    const res = (await run()) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    const expected = {
      Env: {
        Driver: 'sqlite3',
        URL: {
          Scheme: 'sqlite',
          Opaque: '',
          User: null,
          Host: 'test',
          Path: '',
          RawPath: '',
          ForceQuery: false,
          RawQuery: 'mode=memory&cache=shared&_fk=1',
          Fragment: '',
          RawFragment: '',
          DSN: 'file:test?mode=memory&cache=shared&_fk=1',
          Schema: 'main'
        },
        Dir: '__tests__/testdata/sqlite-with-diagnostics'
      },
      Steps: [
        {
          Name: 'Migration Integrity Check',
          Text: 'File atlas.sum is valid'
        },
        {
          Name: 'Detect New Migration Files',
          Text: 'Found 1 new migration files (from 3 total)'
        },
        {
          Name: 'Replay Migration Files',
          Text: 'Loaded 1 changes on dev database'
        },
        {
          Name: 'Analyze 20220619130911_second.sql',
          Text: '1 reports were found in analysis',
          Result: {
            Name: '20220619130911_second.sql',
            Text: '-- DROP "tbl2" table\nDROP TABLE tbl2;\n',
            Reports: [
              {
                Text: 'Destructive changes detected in file 20220619130911_second.sql',
                Diagnostics: [
                  {
                    Pos: 21,
                    Text: 'Dropping table "tbl2"'
                  }
                ]
              }
            ]
          }
        }
      ],
      Schema: null,
      Files: [
        {
          Name: '20220619130911_second.sql',
          Text: '-- DROP "tbl2" table\nDROP TABLE tbl2;\n',
          Reports: [
            {
              Text: 'Destructive changes detected in file 20220619130911_second.sql',
              Diagnostics: [
                {
                  Pos: 21,
                  Text: 'Dropping table "tbl2"'
                }
              ]
            }
          ]
        }
      ]
    }
    expect(res.summary).toEqual(expected)
    expect(res.raw).toEqual(JSON.stringify(expected))
  })

  test('successful run with golang migrate format', async () => {
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    process.env['INPUT_DIR-FORMAT'] = 'golang-migrate'
    process.env['INPUT_LATEST'] = '4'
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Success)
    expect(res.summary?.Files).toHaveLength(2)
    expect(res.summary?.Files[0].Name).toEqual('1_initial.up.sql')
    expect(res.summary?.Files[0].Text).toContain('CREATE TABLE tbl')
    expect(res.summary?.Files[0].Error).toBeFalsy()
    expect(res.summary?.Files[1].Name).toEqual('2_second_migration.up.sql')
    expect(res.summary?.Files[1].Text).toContain('CREATE TABLE tbl_2')
    expect(res.summary?.Files[1].Error).toBeFalsy()
    expect(res.summary?.Schema).toBeNull()
  })

  test('fail on wrong migration dir format', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE-NAME>.
    process.env['INPUT_DIR-FORMAT'] = 'incorrect'
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    const res = (await run()) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Failure)
    expect(res.summary?.Files).toEqual([
      {
        Name: '1_initial.down.sql',
        Error: 'executing statement: "DROP TABLE tbl;": no such table: tbl'
      }
    ])
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
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
        'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        INPUT_LATEST: '0',
        GITHUB_WORKSPACE: gitRepo,
        INPUT_DIR: migrationsDir,
        ATLASCI_USER_AGENT: 'test-atlasci-action',
        'INPUT_REPORT-SCHEMA': 'false'
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
    await git.remote(['add', 'origin', gitRepo])
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
    await git.push('origin', baseBranch)
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
    expect(res.summary?.Files).toHaveLength(1)
    expect(res.summary?.Files[0].Name).toBe('20220728131023.sql')
    expect(res.summary?.Files[0].Text).toBe(
      `-- DROP "tbl" table\nDROP TABLE tbl;\n`
    )
    expect(res.summary?.Files[0].Reports).toHaveLength(1)
    expect(res.summary?.Files[0].Reports?.[0].Text).toBe(
      'Destructive changes detected in file 20220728131023.sql'
    )
    expect(res.summary?.Files[0].Reports?.[0].Diagnostics?.[0].Text).toBe(
      `Dropping table "tbl"`
    )
    expect(res.summary?.Schema).toBeNull()
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
        'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        ATLASCI_USER_AGENT: 'test-atlasci-action',
        'INPUT_REPORT-SCHEMA': 'false'
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

  test('fail on atlas known error', async () => {
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
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
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
    actualRequestBody: { [key: string]: string & Variables },
    gqlInterceptor: nock.Interceptor,
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
        'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
        'INPUT_ARIGA-URL': `https://ci.ariga.cloud`,
        'INPUT_ARIGA-TOKEN': `mysecrettoken`,
        ATLASCI_USER_AGENT: 'test-atlasci-action',
        'INPUT_REPORT-SCHEMA': 'false',
        GITHUB_REPOSITORY: 'someProject/someRepo',
        GITHUB_REF_NAME: 'test',
        GITHUB_SHA: '71d0bfc1'
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
    const url = process.env['INPUT_ARIGA-URL'] as string
    gqlInterceptor = nock(url)
      .post('/api/query', function (body) {
        actualRequestBody = body
        return body
      })
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
    nock.cleanAll()
    actualRequestBody = {}
  })

  test('successfully', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
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
    expect(actualRequestBody).toEqual({
      query: mutation,
      variables: {
        envName: 'CI',
        projectName:
          'someProject/someRepo-__tests__/testdata/sqlite-with-diagnostics',
        url: 'https://github.com/ariga/atlasci-action/pull/1',
        status: 'successful',
        payload: expect.anything()
      },
      operationName: 'CreateReportInput'
    })
    const payloadParsed = JSON.parse(actualRequestBody.variables.payload)
    expect(payloadParsed).toEqual({
      Env: res.summary?.Env,
      Steps: res.summary?.Steps,
      Schema: null,
      Files: res.summary?.Files
    })
  })

  test('successfully with schema', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    process.env['INPUT_REPORT-SCHEMA'] = 'true'
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
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
    expect(actualRequestBody).toEqual({
      query: mutation,
      variables: {
        envName: 'CI',
        projectName:
          'someProject/someRepo-__tests__/testdata/sqlite-with-diagnostics',
        url: 'https://github.com/ariga/atlasci-action/pull/1',
        status: 'successful',
        payload: expect.anything()
      },
      operationName: 'CreateReportInput'
    })
    const payloadParsed = JSON.parse(actualRequestBody.variables.payload)
    expect(payloadParsed).toEqual({
      Env: res.summary?.Env,
      Steps: res.summary?.Steps,
      Schema: {
        Current:
          'table "tbl2" {\n  schema = schema.main\n  column "col" {\n    null = false\n    type = int\n  }\n}\nschema "main" {\n}\n',
        Desired: 'schema "main" {\n}\n'
      },
      Files: res.summary?.Files
    })
  })
})
