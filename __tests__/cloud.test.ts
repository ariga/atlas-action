import { expect } from '@jest/globals'
import { AtlasResult, ExitCodes } from '../src/atlas'
import { getCloudURL, mutation, reportToCloud } from '../src/cloud'
import * as http from '@actions/http-client'
import nock from 'nock'
import * as core from '@actions/core'
import * as gql from 'graphql-request'
import { createTestEnv } from './env'
import { OptionsFromEnv, Options } from '../src/input'
import path from 'path'
import { RunStatus } from '../types/types'

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
        'INPUT_CLOUD-URL': `https://api.atlasgo.cloud`,
        'INPUT_CLOUD-TOKEN': `mysecrettoken`,
        ATLASCI_USER_AGENT: 'test-atlasci-action'
      }
    })
    process.env = env
    cleanupFn = cleanup
    gqlInterceptor = nock(process.env['INPUT_CLOUD-URL'] as string)
      .post('/api/query')
      .matchHeader(
        'Authorization',
        `Bearer ${process.env['INPUT_CLOUD-TOKEN']}`
      )
      .matchHeader('User-Agent', process.env.ATLASCI_USER_AGENT as string)
  })

  afterEach(async () => {
    await cleanupFn()
    spyOnWarning.mockReset()
    nock.cleanAll()
  })

  test('correct cloud url', async () => {
    let opts: Options = OptionsFromEnv(process.env)
    expect(getCloudURL(opts)).toEqual(
      `${process.env['INPUT_CLOUD-URL']}/api/query`
    )
  })

  test('successful', async () => {
    const cloudURL = 'https://ariga.cloud.test/ci-runs/8589934593'
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {
          Driver: 'MySQL',
          Dir: 'migrations'
        },
        Steps: null,
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(http.HttpCodes.OK, {
      data: {
        createReport: {
          runID: '8589934593',
          url: cloudURL,
          cloudReports: [
            {
              text: 'Cloud reports',
              diagnostics: [
                {
                  text: 'diag'
                }
              ]
            }
          ]
        }
      }
    })
    const spyOnRequest = jest.spyOn(gql, 'request')
    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    const expected = {
      createReport: {
        runID: '8589934593',
        url: cloudURL,
        cloudReports: [
          {
            text: 'Cloud reports',
            diagnostics: [
              {
                text: 'diag'
              }
            ]
          }
        ]
      }
    }
    expect(payload).toBeTruthy()
    expect(payload.result).toEqual(expected)
    expect(scope.isDone()).toBeTruthy()
    expect(spyOnRequest).toBeCalledTimes(1)

    expect(spyOnRequest).toBeCalledWith(
      'https://api.atlasgo.cloud/api/query',
      mutation,
      {
        input: {
          branch: process.env.GITHUB_HEAD_REF,
          commit: process.env.GITHUB_SHA,
          envName: 'CI',
          payload: '[{"Name":"test","Text":"test"}]',
          projectName: path.join(
            process.env.GITHUB_REPOSITORY ?? '',
            res.summary?.Env?.Dir ?? ''
          ),
          status: RunStatus.Successful,
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
        Env: {
          Driver: 'MySQL',
          Dir: 'migrations'
        },
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
      'https://api.atlasgo.cloud/api/query',
      expect.anything(),
      expect.anything(),
      expect.anything()
    )
    expect(spyOnWarning).toHaveBeenCalledTimes(3)
    expect(spyOnWarning).toHaveBeenNthCalledWith(
      2,
      expect.stringContaining(
        'Received error: Error: GraphQL Error (Code: 500)'
      )
    )
    expect(spyOnWarning).toHaveBeenNthCalledWith(
      3,
      'Failed reporting to Atlas Cloud: Internal server error'
    )
  })

  test('http unauthorized', async () => {
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {
          Driver: 'MySQL',
          Dir: 'migrations'
        },
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
    expect(spyOnWarning).toHaveBeenCalledTimes(3)
    expect(spyOnWarning).toHaveBeenCalledWith(
      'Failed reporting to Atlas Cloud: Invalid Token'
    )
    expect(payload.prettyErr).toEqual('Invalid Token')
    expect(payload.err).toBeDefined()
  })

  test('ignore when missing token', async () => {
    process.env['INPUT_CLOUD-TOKEN'] = ''
    const res: AtlasResult = {
      exitCode: ExitCodes.Success,
      raw: '[{"Name":"test","Text":"test"}]',
      summary: {
        Files: [{ Name: 'test', Text: 'test' }],
        Env: {
          Driver: 'MySQL',
          Dir: 'migrations'
        },
        Steps: [],
        Schema: null
      }
    }
    const spyOnRequest = jest.spyOn(gql, 'request')
    let opts: Options = OptionsFromEnv(process.env)
    const payload = await reportToCloud(opts, res)
    expect(spyOnRequest).not.toHaveBeenCalled()
    expect(spyOnWarning).toHaveBeenCalledTimes(2)
    expect(spyOnWarning).toHaveBeenCalledWith(
      'Skipping report to cloud missing cloud-token input'
    )
  })
})
