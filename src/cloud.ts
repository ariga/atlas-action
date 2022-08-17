import * as github from '@actions/github'
import { getInput, info, setSecret, warning } from '@actions/core'
import { AtlasResult, ExitCodes, getMigrationDir, getUserAgent } from './atlas'
import * as url from 'url'
import { ClientError, gql, request } from 'graphql-request'
import * as http from '@actions/http-client'
import os from 'os'

const LINUX_ARCH = 'linux-amd64'
const APPLE_ARCH = 'darwin-amd64'
export const BASE_ADDRESS = 'https://release.ariga.io'
export const S3_FOLDER = 'atlas'
export const LATEST_RELEASE = 'latest'
export const ARCHITECTURE = os.platform() === 'darwin' ? APPLE_ARCH : LINUX_ARCH

export const mutation = gql`
  mutation CreateReportInput($input: CreateReportInput!) {
    createReport(input: $input) {
      runID
      url
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

export enum Status {
  Success = 'SUCCESSFUL',
  Failure = 'FAILED'
}

function getMutationVariables(res: AtlasResult): CreateReportInput {
  const { GITHUB_REPOSITORY: repository, GITHUB_SHA: commitID } = process.env
  // GITHUB_HEAD_REF is set only on pull requests
  // GITHUB_REF_NAME is the correct branch when running in a branch, on pull requests it's the PR number.
  const sourceBranch =
    process.env.GITHUB_HEAD_REF || process.env.GITHUB_REF_NAME
  const migrationDir = getMigrationDir().replace('file://', '')
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
      projectName: `${repository}-${migrationDir}`,
      branch: sourceBranch ?? 'unknown',
      commit: commitID ?? 'unknown',
      url: github?.context?.payload?.pull_request?.html_url ?? 'unknown',
      status:
        res.exitCode === ExitCodes.Success ? Status.Success : Status.Failure,
      payload: res.raw
    }
  }
}

export async function reportToCloud(
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
      getMutationVariables(res),
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

export function getCloudURL(): string {
  return new url.URL('/api/query', getInput(`ariga-url`)).toString()
}

function getHeaders(token: string): { [p: string]: string } {
  return {
    Authorization: `Bearer ${token}`,
    ...getUserAgent()
  }
}

export function getDownloadURL(version: string): URL {
  return new URL(
    `${BASE_ADDRESS}/${S3_FOLDER}/atlas-${ARCHITECTURE}-${version}`
  )
}
