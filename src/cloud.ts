import * as github from '@actions/github'
import { context } from '@actions/github'
import { getInput, info, setSecret, warning } from '@actions/core'
import { AtlasResult, ExitCodes, getMigrationDir, getUserAgent } from './atlas'
import * as url from 'url'
import { ClientError, gql, request } from 'graphql-request'
import * as http from '@actions/http-client'

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

function getMutationVariables(res: AtlasResult): {
  [p: string]: string | number | undefined
} {
  const {
    GITHUB_REPOSITORY: repository,
    GITHUB_SHA: commitID,
    GITHUB_REF_NAME: sourceBranch
  } = process.env
  const migrationDir = getMigrationDir().replace('file://', '')
  return {
    envName: 'CI',
    projectName: `${repository}-${migrationDir}`,
    branch: sourceBranch,
    commit: commitID,
    url: github?.context?.payload?.pull_request?.html_url,
    status: res.exitCode === ExitCodes.Success ? 'successful' : 'failed',
    payload: res.raw
  }
}

export async function reportToCloud(
  res: AtlasResult
): Promise<CreateReportPayload | void> {
  if (context.eventName !== `pull_request`) {
    warning(`Skipping report to cloud for non pull request trigger`)
    return
  }
  const token = getInput('ariga-token')
  if (!token) {
    warning(`Skipping report to cloud missing ariga-token input`)
    return
  }
  info(`Reporting to cloud`)
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
    warning(`Failed reporting to Ariga Cloud: ${errMsg}`)
  }
}

export function getCloudURL(): string {
  return new url.URL('/api/query', getInput(`cloud-url`)).toString()
}

function getHeaders(token: string): { [p: string]: string } {
  return {
    Authorization: `Bearer ${token}`,
    ...getUserAgent()
  }
}
