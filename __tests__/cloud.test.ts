import { expect } from '@jest/globals'
import { AtlasResult, ExitCodes, getMigrationDir } from '../src/atlas'
import { getCloudURL, mutation, reportToCloud, Status } from '../src/cloud'
import * as http from '@actions/http-client'
import * as github from '@actions/github'
import nock from 'nock'
import * as core from '@actions/core'
import * as gql from 'graphql-request'
import { createTestENV, originalENV } from './env'
import { rm } from 'fs/promises'

jest.setTimeout(30000)

describe('report to cloud', () => {
  let spyOnWarning: jest.SpyInstance
  let gqlInterceptor: nock.Interceptor
  const originalContext = { ...github.context }

  beforeEach(async () => {
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
    spyOnWarning = jest.spyOn(core, 'warning')
    process.env = await createTestENV({
      GITHUB_REPOSITORY: 'someProject/someRepo',
      GITHUB_SHA: '71d0bfc1',
      INPUT_DIR: 'migrations',
      'INPUT_ARIGA-URL': `https://ci.ariga.cloud`,
      'INPUT_ARIGA-TOKEN': `mysecrettoken`,
      ATLASCI_USER_AGENT: 'test-atlasci-action'
    })
    gqlInterceptor = nock(process.env['INPUT_ARIGA-URL'] as string)
      .post('/api/query')
      .matchHeader(
        'Authorization',
        `Bearer ${process.env['INPUT_ARIGA-TOKEN']}`
      )
      .matchHeader('User-Agent', process.env.ATLASCI_USER_AGENT as string)
  })

  afterEach(async () => {
    Object.defineProperty(github, 'context', {
      value: originalContext
    })
    spyOnWarning.mockReset()
    if (process.env.RUNNER_TEMP) {
      await rm(process.env.RUNNER_TEMP, { recursive: true })
    }
    process.env = { ...originalENV }
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
        Steps: {},
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

    const payload = await reportToCloud(res)
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
          projectName: `${process.env.GITHUB_REPOSITORY}-${getMigrationDir()}`,
          status: Status.Success,
          url: 'https://github.com/ariga/atlasci-action/pull/1'
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
        Steps: {},
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(
      http.HttpCodes.InternalServerError,
      'Internal server error'
    )

    const spyOnRequest = jest.spyOn(gql, 'request')

    await reportToCloud(res)
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
        Steps: {},
        Schema: null
      }
    }
    const scope = gqlInterceptor.reply(
      http.HttpCodes.Unauthorized,
      'Unauthorized'
    )
    const spyOnRequest = jest.spyOn(gql, 'request')

    await reportToCloud(res)
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
        Steps: {},
        Schema: null
      }
    }
    const spyOnRequest = jest.spyOn(gql, 'request')
    await reportToCloud(res)
    expect(spyOnRequest).not.toHaveBeenCalled()
    expect(spyOnWarning).toHaveBeenCalledTimes(1)
    expect(spyOnWarning).toHaveBeenCalledWith(
      'Skipping report to cloud missing ariga-token input'
    )
  })
})
