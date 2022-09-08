import { expect } from '@jest/globals'
import { AtlasResult, ExitCodes, getMigrationDir } from '../src/atlas'
import { getCloudURL, mutation, reportToCloud, Status } from '../src/cloud'
import * as http from '@actions/http-client'
import nock from 'nock'
import * as core from '@actions/core'
import * as gql from 'graphql-request'
import { createTestEnv } from './env'
import { OptionsFromEnv, Options } from '../src/input'

jest.setTimeout(30000)

describe('report to cloud', () => {
  let spyOnWarning: jest.SpyInstance,
    gqlInterceptor: nock.Interceptor,
    cleanupFn: () => Promise<void>

  beforeEach(async () => {
    spyOnWarning = jest.spyOn(core, 'warning')
    const { env, cleanup } = await createTestEnv({
      override: {
        GITHUB_REPOSITORY: 'someProject/someRepo',
        GITHUB_SHA: '71d0bfc1',
        INPUT_DIR: 'migrations',
        'INPUT_ARIGA-URL': `https://ci.ariga.cloud`,
        'INPUT_ARIGA-TOKEN': `mysecrettoken`,
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    })
    process.env = env
    cleanupFn = cleanup
    gqlInterceptor = nock(process.env['INPUT_ARIGA-URL'] as string)
      .post('/api/query')
      .matchHeader(
        'Authorization',
        `Bearer ${process.env['INPUT_ARIGA-TOKEN']}`
      )
      .matchHeader('User-Agent', process.env.ATLASCI_USER_AGENT as string)
  })

  afterEach(async () => {
    await cleanupFn()
    spyOnWarning.mockReset()
    nock.cleanAll()
  })

  test('correct cloud url', async () => {
    expect(getCloudURL()).toEqual(`${process.env['INPUT_ARIGA-URL']}/api/query`)
  })

  test('successful', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {},
        Steps: null,
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
      data: {
        createReport: {
          runID: '8589934593',
          url: cloudURL
        }
      }
    })
    const spyOnRequest = jest.spyOn(gql, 'request')
    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    expect(payload).toBeTruthy()
    expect(payload?.createReport.url).toEqual(cloudURL)
    expect(payload?.createReport.runID).toEqual('8589934593')
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnRequest).toBeCalledTimes(1)
    expect(spyOnRequest).toBeCalledWith(
      'https://ci.ariga.cloud/api/query',
      mutation,
      {
        input: {
          branch: process.env.GITHUB_HEAD_REF,
          commit: process.env.GITHUB_SHA,
          envName: 'CI',
          payload: '[{"Name":"test","Text":"test"}]',
          projectName: `${process.env.GITHUB_REPOSITORY}/${getMigrationDir(
            opts.dir
          )}`,
          status: Status.Success,
          url: 'https://github.com/ariga/atlas-action/pull/1'
        }
      },
      {
        Authorization: 'Bearer mysecrettoken',
        'User-Agent': 'test-atlasci-action'
      }
    )
  })

  test('http internal server error', async () => {
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {},
        Steps: null,
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(
      http.HttpCodes.InternalServerError,
      'Internal server error'
    )

    const spyOnRequest = jest.spyOn(gql, 'request')

    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnRequest).toBeCalledTimes(1)
    expect(spyOnRequest).toHaveBeenCalledWith(
      'https://ci.ariga.cloud/api/query',
      expect.anything(),
      expect.anything(),
      expect.anything()
    )
    expect(spyOnWarning).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenNthCalledWith(
      1,
      expect.stringContaining(
        'Received error: Error: GraphQL Error (Code: 500)'
      )
    )
    expect(spyOnWarning).toHaveBeenNthCalledWith(
      2,
      'Failed reporting to Ariga Cloud: Internal server error'
    )
  })

  test('http unauthorized', async () => {
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {},
        Steps: [],
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(
      http.HttpCodes.Unauthorized,
      'Unauthorized'
    )
    const spyOnRequest = jest.spyOn(gql, 'request')

    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnRequest).toBeCalledTimes(1)
    expect(spyOnWarning).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledWith(
      'Failed reporting to Ariga Cloud: Invalid Token'
    )
  })

  test('ignore when missing token', async () => {
    process.env['INPUT_ARIGA-TOKEN'] = ''
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {},
        Steps: [],
        Schema: null
      }
    }
    const spyOnRequest = jest.spyOn(gql, 'request')
    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    expect(spyOnRequest).not.toHaveBeenCalled()
    expect(spyOnWarning).toHaveBeenCalledTimes(1)
    expect(spyOnWarning).toHaveBeenCalledWith(
      'Skipping report to cloud missing ariga-token input'
    )
  })
})
