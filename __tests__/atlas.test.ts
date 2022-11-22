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
import { getInput } from '@actions/core'
import nock from 'nock'
import * as http from '@actions/http-client'
import { AtlasResult, ExitCodes, installAtlas } from '../src/atlas'
import {
  ARCHITECTURE,
  BASE_ADDRESS,
  getDownloadURL,
  mutation,
  S3_FOLDER,
  Status
} from '../src/cloud'
import { Variables } from 'graphql-request/src/types'
import { createTestEnv, GithubEventName } from './env'
import { OptionsFromEnv, RunInput } from '../src/input'

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

describe('install', () => {
  let cleanupFn: () => Promise<void>
  let version: string

  beforeEach(async () => {
    const { env, cleanup } = await createTestEnv()
    process.env = env
    version = getInput('atlas-version')
    cleanupFn = cleanup
  })

  afterEach(async () => {
    await cleanupFn()
    nock.cleanAll()
  })

  test('install the latest version of atlas', async () => {
    const bin = await installAtlas(version)
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
    const url = getDownloadURL(version)
    expect(url.toString()).toEqual(
      `https://release.ariga.io/atlas/atlas-${ARCHITECTURE}-${version}?test=1`
    )
    const content = 'OK'
    const scope = nock(`${url.protocol}//${url.host}`)
      .get(`${url.pathname}${url.search}`)
      .matchHeader('user-agent', 'actions/tool-cache')
      .reply(200, content)
    const bin = await installAtlas(version)
    expect(scope.isDone()).toBeTruthy()
    await expect(stat(bin)).resolves.toBeTruthy()
    await expect(readFile(bin)).resolves.toEqual(Buffer.from(content))
  })
})

describe('run with "latest" flag', () => {
  let spyOnSetFailed: jest.SpyInstance, cleanupFn: () => Promise<void>

  beforeEach(async () => {
    const { env, cleanup } = await createTestEnv({
      override: {
        INPUT_LATEST: '1',
        'INPUT_SCHEMA-INSIGHTS': 'false'
      }
    })
    process.env = env
    cleanupFn = cleanup
    spyOnSetFailed = jest.spyOn(core, 'setFailed')
  })

  afterEach(async () => {
    spyOnSetFailed.mockReset()
    await cleanupFn()
  })

  test('successful no issues', async () => {
    if (!process.env.RUNNER_TEMP) {
      throw new Error('RUNNER_TEMP is not set')
    }
    const migrationsDir = path.join(process.env.RUNNER_TEMP, 'migrations')
    await mkdir(migrationsDir)
    process.env.INPUT_DIR = migrationsDir
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
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
          OmitHost: false,
          ForceQuery: false,
          RawQuery: 'mode=memory&cache=shared&_fk=1',
          Fragment: '',
          RawFragment: '',
          Schema: 'main'
        },
        // This value is random and changes on every run.
        Dir: res.summary?.Env.Dir
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
    let input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
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
          OmitHost: false,
          ForceQuery: false,
          RawQuery: 'mode=memory&cache=shared&_fk=1',
          Fragment: '',
          RawFragment: '',
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

    let input = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
  })

  test('successful run with golang migrate format', async () => {
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    process.env['INPUT_DIR-FORMAT'] = 'golang-migrate'
    process.env['INPUT_LATEST'] = '4'
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Success)
    expect(res.summary?.Files).toEqual([
      {
        Name: '1_initial.up.sql',
        Text: 'CREATE TABLE tbl\n(\n    col INT\n);'
      },
      {
        Name: '2_second_migration.up.sql',
        Text: 'CREATE TABLE tbl_2 (col INT);'
      }
    ])
    expect(res.summary?.Schema).toBeNull()
  })

  test('fail on wrong migration dir format', async () => {
    // Actions creates an environment variables for inputs (in action.yaml), syntax: INPUT_<VARIABLE-NAME>.
    process.env['INPUT_DIR-FORMAT'] = 'incorrect'
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'golang-migrate')
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toEqual(ExitCodes.Failure)
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
    expect(spyOnSetFailed).toHaveBeenCalledWith(
      'Atlas failed with code 1: Error: unknown dir format "incorrect"\n'
    )
  })
})

describe('run with mock repo', () => {
  let gitRepo: string, cleanupFn: () => Promise<void>

  beforeEach(async () => {
    const changesBranch = 'changes'
    const baseBranch = 'master'
    gitRepo = await mkdtemp(`${tmpdir()}${path.sep}`)
    const migrationsDir = path.join(gitRepo, 'migrations')
    await mkdir(migrationsDir)
    const { env, cleanup } = await createTestEnv({
      override: {
        GITHUB_BASE_REF: baseBranch,
        INPUT_LATEST: '0',
        GITHUB_WORKSPACE: gitRepo,
        INPUT_DIR: migrationsDir,
        'INPUT_SCHEMA-INSIGHTS': 'false'
      }
    })
    process.env = env
    cleanupFn = cleanup
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
    await cleanupFn()
  })

  test('successful', async () => {
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Failure)
    expect(res.summary?.Files).toHaveLength(1)
    expect(res.summary?.Files[0].Name).toBe('20220728131023.sql')
    expect(res.summary?.Files[0].Text).toBe(
      `-- DROP "tbl" table\nDROP TABLE tbl;\n`
    )
    expect(res.summary?.Files[0].Reports).toHaveLength(1)
    expect(res.summary?.Files[0].Reports?.[0].Text).toBe(
      'destructive changes detected'
    )
    expect(res.summary?.Files[0].Reports?.[0].Diagnostics?.[0].Text).toBe(
      `Dropping table "tbl"`
    )
    expect(res.summary?.Schema).toBeNull()
  })
})

describe('report to GitHub', () => {
  let spyOnNotice: jest.SpyInstance,
    spyOnError: jest.SpyInstance,
    spyOnSetFailed: jest.SpyInstance,
    cleanupFn: () => Promise<void>

  beforeEach(async () => {
    const { env, cleanup } = await createTestEnv({
      override: {
        INPUT_LATEST: '1',
        'INPUT_SCHEMA-INSIGHTS': 'false'
      }
    })
    process.env = env
    cleanupFn = cleanup
    spyOnNotice = jest.spyOn(core, 'notice')
    spyOnError = jest.spyOn(core, 'error')
    spyOnSetFailed = jest.spyOn(core, 'setFailed')
  })

  afterEach(async () => {
    spyOnNotice.mockReset()
    spyOnError.mockReset()
    spyOnSetFailed.mockReset()
    await cleanupFn()
  })

  test('regular', async () => {
    const spyOnNotice = jest.spyOn(core, 'notice')
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(spyOnNotice).toHaveBeenCalledTimes(1)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnNotice).toHaveBeenCalledWith(
      'Adding a unique index "uniq_name" on table "users" might fail in case column "name" contains duplicate entries (MF101)\n\nDetails: https://atlasgo.io/lint/analyzers#MF101',
      {
        file: '__tests__/testdata/sqlite-with-diagnostics/20220823075011_uniq_name.sql',
        startLine: 2,
        title: 'data dependent changes detected'
      }
    )
  })

  test('fail on atlas known error', async () => {
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-broken-file'
    )
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Failure)
    expect(spyOnNotice).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(1)
    expect(spyOnError).toHaveBeenCalledWith(
      'executing statement: near "BAD": syntax error',
      {
        file: '__tests__/testdata/sqlite-broken-file/20220318104614_initial.sql',
        startLine: 1
      }
    )
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
  })

  test('atlas unknown error - no report', async () => {
    process.env.INPUT_DIR = path.join('__tests__', 'testdata', 'nothing-here')
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Failure)
    expect(spyOnNotice).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnSetFailed).toHaveBeenCalledTimes(1)
    expect(spyOnSetFailed).toHaveBeenCalledWith(
      'Atlas failed with code 1: Error: sql/migrate: stat __tests__/testdata/nothing-here: no such file or directory\n'
    )
  })
})

describe('all reports with pull request', () => {
  let actualRequestBody: { [key: string]: string & Variables },
    gqlInterceptor: nock.Interceptor,
    spyOnNotice: jest.SpyInstance,
    spyOnError: jest.SpyInstance,
    spyOnWarning: jest.SpyInstance,
    cleanupFn: () => Promise<void>

  beforeEach(async () => {
    const { env, cleanup } = await createTestEnv({
      override: {
        INPUT_LATEST: '1',
        'INPUT_ARIGA-TOKEN': `mysecrettoken`,
        'INPUT_SCHEMA-INSIGHTS': 'false',
        GITHUB_REPOSITORY: 'someProject/someRepo',
        GITHUB_SHA: '71d0bfc1'
      }
    })
    process.env = env
    cleanupFn = cleanup
    spyOnNotice = jest.spyOn(core, 'notice')
    spyOnError = jest.spyOn(core, 'error')
    spyOnWarning = jest.spyOn(core, 'warning')
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
    spyOnNotice.mockReset()
    spyOnError.mockReset()
    spyOnWarning.mockReset()
    await cleanupFn()
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
        runID: '8589934593',
        url: cloudURL
      }
    })
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnNotice).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(spyOnNotice).toHaveBeenNthCalledWith(
      1,
      'Adding a unique index "uniq_name" on table "users" might fail in case column "name" contains duplicate entries (MF101)\n\nDetails: https://atlasgo.io/lint/analyzers#MF101',
      {
        file: '__tests__/testdata/sqlite-with-diagnostics/20220823075011_uniq_name.sql',
        startLine: 2,
        title: 'data dependent changes detected'
      }
    )
    expect(spyOnNotice).toHaveBeenNthCalledWith(
      2,
      'For full report visit: https://ariga.cloud.test/ci-runs/8589934593'
    )
    expect(actualRequestBody).toEqual({
      query: mutation,
      variables: {
        branch: 'test-pr-trigger',
        commit: '71d0bfc1',
        envName: 'CI',
        projectName:
          'someProject/someRepo/__tests__/testdata/sqlite-with-diagnostics',
        url: 'https://github.com/ariga/atlas-action/pull/1',
        status: Status.Success,
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
    process.env['INPUT_SCHEMA-INSIGHTS'] = 'true'
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
      data: {
        runID: '8589934593',
        url: cloudURL
      }
    })
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnNotice).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(actualRequestBody).toEqual({
      query: mutation,
      variables: {
        branch: 'test-pr-trigger',
        commit: '71d0bfc1',
        envName: 'CI',
        projectName:
          'someProject/someRepo/__tests__/testdata/sqlite-with-diagnostics',
        url: 'https://github.com/ariga/atlas-action/pull/1',
        status: Status.Success,
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
          'table "users" {\n  schema = schema.main\n  column "id" {\n    null = false\n    type = int\n  }\n  column "name" {\n    null = false\n    type = varchar\n  }\n}\nschema "main" {\n}\n',
        Desired:
          'table "users" {\n  schema = schema.main\n  column "id" {\n    null = false\n    type = int\n  }\n  column "name" {\n    null = false\n    type = varchar\n  }\n  index "uniq_name" {\n    unique  = true\n    columns = [column.name]\n  }\n}\nschema "main" {\n}\n'
      },
      Files: res.summary?.Files
    })
  })
})

describe('all reports with push (branch)', () => {
  let actualRequestBody: { [key: string]: string & Variables },
    gqlInterceptor: nock.Interceptor,
    spyOnNotice: jest.SpyInstance,
    spyOnError: jest.SpyInstance,
    spyOnWarning: jest.SpyInstance,
    cleanupFn: () => Promise<void>

  beforeEach(async () => {
    const { env, cleanup } = await createTestEnv({
      override: {
        INPUT_LATEST: '1',
        'INPUT_ARIGA-TOKEN': `mysecrettoken`,
        'INPUT_SCHEMA-INSIGHTS': 'false',
        GITHUB_REPOSITORY: 'someProject/someRepo',
        GITHUB_SHA: '71d0bfc1'
      },
      eventName: GithubEventName.Push
    })
    process.env = env
    cleanupFn = cleanup
    spyOnNotice = jest.spyOn(core, 'notice')
    spyOnError = jest.spyOn(core, 'error')
    spyOnWarning = jest.spyOn(core, 'warning')
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
    spyOnNotice.mockReset()
    spyOnError.mockReset()
    spyOnWarning.mockReset()
    await cleanupFn()
    nock.cleanAll()
    actualRequestBody = {}
  })

  test('successfully with schema', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    process.env.INPUT_DIR = path.join(
      '__tests__',
      'testdata',
      'sqlite-with-diagnostics'
    )
    process.env['INPUT_SCHEMA-INSIGHTS'] = 'true'
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
      data: {
        runID: '8589934593',
        url: cloudURL
      }
    })
    const input: RunInput = {
      opts: OptionsFromEnv(process.env),
      pr: undefined
    }
    const res = (await run(input)) as AtlasResult
    expect(res.exitCode).toBe(ExitCodes.Success)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnNotice).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledTimes(0)
    expect(spyOnError).toHaveBeenCalledTimes(0)
    expect(actualRequestBody).toEqual({
      query: mutation,
      variables: {
        branch: 'test-pr-trigger',
        commit: '71d0bfc1',
        envName: 'CI',
        projectName:
          'someProject/someRepo/__tests__/testdata/sqlite-with-diagnostics',
        url: 'https://github.com/ariga/atlas-action',
        status: Status.Success,
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
          'table "users" {\n  schema = schema.main\n  column "id" {\n    null = false\n    type = int\n  }\n  column "name" {\n    null = false\n    type = varchar\n  }\n}\nschema "main" {\n}\n',
        Desired:
          'table "users" {\n  schema = schema.main\n  column "id" {\n    null = false\n    type = int\n  }\n  column "name" {\n    null = false\n    type = varchar\n  }\n  index "uniq_name" {\n    unique  = true\n    columns = [column.name]\n  }\n}\nschema "main" {\n}\n'
      },
      Files: res.summary?.Files
    })
  })
})
