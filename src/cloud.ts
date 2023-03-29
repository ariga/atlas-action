import * as github from '@actions/github'
import { getIDToken, info, setSecret, warning } from '@actions/core'
import { AtlasResult, ExitCodes, getUserAgent } from './atlas'
import * as url from 'url'
import { ClientError, gql, request } from 'graphql-request'
import * as http from '@actions/http-client'
import os from 'os'
import { Options } from './input'
import { Mutation, MutationCreateReportArgs, RunStatus } from '../types/types'

const LINUX_ARCH = 'linux-amd64'
const APPLE_ARCH = 'darwin-amd64'
export const BASE_ADDRESS = 'https://release.ariga.io'
export const S3_FOLDER = 'atlas'
export const ARCHITECTURE = os.platform() === 'darwin' ? APPLE_ARCH : LINUX_ARCH
export const BASE_CLOUD_URL = 'https://api.atlasgo.cloud'

export const mutation = gql`
  mutation CreateReportInput($input: CreateReportInput!) {
    createReport(input: $input) {
      runID
      url
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
`

export enum Status {
  Success = 'SUCCESSFUL',
  Failure = 'FAILED'
}
function getMutationVariables(
  opts: Options,
  res: AtlasResult
): MutationCreateReportArgs {
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
        res.exitCode === ExitCodes.Success
          ? RunStatus.Successful
          : RunStatus.Failed,
      payload: res.raw
    }
  }
}

export type CloudReport = {
  err?: Error
  prettyErr?: string
  result?: Mutation
}

export async function reportToCloud(
  opts: Options,
  res: AtlasResult
): Promise<CloudReport> {
  let token = opts.cloudToken
  if (!token) {
    if (opts.cloudPublic) {
      try {
        token = await getIDToken('ariga://atlas-ci-action')
      } catch (e) {
        warning('`id-token: write` permission is required to report to cloud')
        return {} as CloudReport
      }
    }
    warning(`Skipping report to cloud missing cloud-token input`)
    return {} as CloudReport
  }
  info(`Reporting to cloud: ${getCloudURL(opts)}`)
  setSecret(token)
  const rep: CloudReport = {}
  try {
    rep.result = await request<Mutation>(
      getCloudURL(opts),
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
    warning(`Failed reporting to Atlas Cloud: ${errMsg}`)
    if (e instanceof Error) {
      rep.err = e
    } else {
      rep.err = Error(`${e}`)
    }
    rep.prettyErr = `${errMsg}`
  }
  return rep
}

export function getCloudURL(opts: Options): string {
  let base = opts.cloudURL
  if (opts.cloudURL === '' || !opts.cloudURL) {
    base = BASE_CLOUD_URL
  }
  return new url.URL('/query', base).toString()
}

function getHeaders(token: string): { [p: string]: string } {
  return {
    Authorization: `Bearer ${token}`,
    ...getUserAgent()
  }
}

export function getDownloadURL(opts: Options): URL {
  const url = new URL(
    `${BASE_ADDRESS}/${S3_FOLDER}/atlas-${ARCHITECTURE}-${opts.atlasVersion}`
  )
  const origin = new URL(getCloudURL(opts)).origin
  if (origin !== BASE_CLOUD_URL) {
    url.searchParams.set('test', '1')
  }
  return url
}
