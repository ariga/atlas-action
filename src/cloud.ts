import * as github from '@actions/github'
import { getInput, info, setSecret, warning } from '@actions/core'
import { AtlasResult, ExitCodes, getUserAgent } from './atlas'
import * as url from 'url'
import { ClientError, gql, request } from 'graphql-request'
import * as http from '@actions/http-client'
import os from 'os'
import { Options } from './input'

const LINUX_ARCH = 'linux-amd64'
const APPLE_ARCH = 'darwin-amd64'
export const BASE_ADDRESS = 'https://release.ariga.io'
export const S3_FOLDER = 'atlas'
export const ARCHITECTURE = os.platform() === 'darwin' ? APPLE_ARCH : LINUX_ARCH

export const mutation = gql`
  mutation CreateReportInput($input: CreateReportInput!) {
    createReport(input: $input) {
      runID
      url
    }
  }
`

export const query = gql`
  query cloudReports($id: ID!) {
    node(id: $id) {
      ... on Run {
        cloudReports {
          text
          diagnostics {
            text
            code
            pos
          }
        }
      }
    }
  }
`

interface CreateReportPayload {
  createReport: {
    runID: string
    url: string
  }
}

type CreateReportInput = {
  input: {
    payload: string
    envName: string
    commit: string
    projectName: string
    branch: string
    url: string
    status: string
  }
}

export interface CloudReportsPayload {
  node: {
    cloudReports: {
      text: string
      diagnostics: {
        text: string
        code: string
        pos: number
      }[]
    }[]
  }
}

export enum Status {
  Success = 'SUCCESSFUL',
  Failure = 'FAILED'
}

function getMutationVariables(
  opts: Options,
  res: AtlasResult
): CreateReportInput {
  const { GITHUB_REPOSITORY: repository, GITHUB_SHA: commitID } = process.env
  // GITHUB_HEAD_REF is set only on pull requests
  // GITHUB_REF_NAME is the correct branch when running in a branch, on pull requests it's the PR number.
  const sourceBranch =
    process.env.GITHUB_HEAD_REF || process.env.GITHUB_REF_NAME
  const migrationDir = res.summary?.Env.Dir.replace('file://', '') || ''
  info(
    `Run metadata: ${JSON.stringify(
      { repository, commitID, sourceBranch, migrationDir },
      null,
      2
    )}`
  )
  return {
    input: {
      envName: 'CI',
      projectName: `${repository}/${migrationDir}`,
      branch: sourceBranch ?? 'unknown',
      commit: commitID ?? 'unknown',
      url:
        github?.context?.payload?.pull_request?.html_url ??
        github?.context?.payload?.repository?.html_url ??
        'unknown',
      status:
        res.exitCode === ExitCodes.Success ? Status.Success : Status.Failure,
      payload: res.raw
    }
  }
}

export async function reportToCloud(
  opts: Options,
  res: AtlasResult
): Promise<CreateReportPayload | void> {
  const token = getInput('ariga-token')
  if (!token) {
    warning(`Skipping report to cloud missing ariga-token input`)
    return
  }
  info(`Reporting to cloud: ${getCloudURL()}`)
  setSecret(token)
  try {
    return await request<CreateReportPayload>(
      getCloudURL(),
      mutation,
      getMutationVariables(opts, res),
      getHeaders(token)
    )
  } catch (e) {
    let errMsg = e
    if (e instanceof ClientError) {
      errMsg = e.response.error
      if (e.response.status === http.HttpCodes.Unauthorized) {
        errMsg = `Invalid Token`
      }
    }
    warning(`Received error: ${e}`)
    warning(`Failed reporting to Ariga Cloud: ${errMsg}`)
  }
}

export async function cloudReports(
  runID: string
): Promise<CloudReportsPayload | undefined> {
  const token = getInput('ariga-token')
  if (!token) {
    warning(`Skipping cloud reports missing ariga-token input`)
  }
  setSecret(token)
  try {
    return await request<CloudReportsPayload, { id: string }>(
      getCloudURL(),
      query,
      { id: runID },
      getHeaders(token)
    )
  } catch (e) {
    let errMsg = e
    if (e instanceof ClientError) {
      errMsg = e.response.error
      if (e.response.status === http.HttpCodes.Unauthorized) {
        errMsg = `Invalid Token`
      }
    }
    warning(`Received error: ${e}`)
    warning(`Failed fetching from Ariga cloud: ${errMsg}`)
  }
}

export function getCloudURL(): string {
  const au = getInput(`ariga-url`)
  return new url.URL(
    '/api/query',
    au === '' ? 'https://ci.ariga.cloud' : au
  ).toString()
}

function getHeaders(token: string): { [p: string]: string } {
  return {
    Authorization: `Bearer ${token}`,
    ...getUserAgent()
  }
}

export function getDownloadURL(version: string): URL {
  const url = new URL(
    `${BASE_ADDRESS}/${S3_FOLDER}/atlas-${ARCHITECTURE}-${version}`
  )
  const origin = new URL(getCloudURL()).origin
  if (origin !== 'https://ci.ariga.cloud') {
    url.searchParams.set('test', '1')
  }
  return url
}
