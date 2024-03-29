import Dict = NodeJS.Dict
import { Context } from '@actions/github/lib/context'
import { warning } from '@actions/core'

export type Options = {
  atlasVersion: string
  dir?: string
  dirName?: string
  dirFormat?: string
  devUrl?: string
  latest?: number
  cloudToken?: string
  cloudPublic?: boolean
  cloudURL?: string
  configPath?: string
  configEnv?: string
  schemaInsights: boolean
  token?: string
  skipCheckForUpdate?: boolean
}

export interface PullRequest {
  // The repository name.
  repo: string
  // The repository org.
  owner: string
  issue_number: number
}

export function PullReqFromContext(ctx: Context): PullRequest | undefined {
  if (ctx.eventName != 'pull_request') {
    return
  }
  if (!ctx.payload.repository) {
    throw new Error('expected repository details to be present')
  }
  if (!ctx.payload.pull_request?.number) {
    throw new Error('expected pr number to be present')
  }
  return {
    repo: ctx.payload.repository.name,
    owner: ctx.payload.repository.owner.login,
    issue_number: ctx.payload.pull_request.number
  }
}

export type RunInput = {
  opts: Options
  pr: PullRequest | undefined
}

export function OptionsFromEnv(env: Dict<string>): Options {
  const input = (name: string): string =>
    env[`INPUT_${name.replace(/ /g, '_').toUpperCase()}`] || ''
  const opts: Options = {
    atlasVersion: input('atlas-version'),
    schemaInsights: true
  }
  if (input('dir')) {
    opts.dir = input('dir')
  }
  if (input('dir-name')) {
    opts.dirName = input('dir-name')
  }
  if (input('dir-format')) {
    opts.dirFormat = input('dir-format')
  }
  if (input('dev-url')) {
    opts.devUrl = input('dev-url')
  }
  if (input('ariga-token')) {
    warning('ariga-token is deprecated, use cloud-token instead')
    opts.cloudToken = input('ariga-token')
  }
  if (input('cloud-token')) {
    opts.cloudToken = input('cloud-token')
  }
  if (input('cloud-public') == 'true') {
    opts.cloudPublic = true
  }
  if (input('latest')) {
    const i = parseInt(input('latest'), 10)
    if (isNaN(i)) {
      throw new Error('expected "latest" to be a number')
    }
    opts.latest = i
  }
  if (input('schema-insights') == 'false') {
    opts.schemaInsights = false
  }
  if (input('ariga-url')) {
    warning('ariga-url is deprecated, use cloud-url instead')
    opts.cloudURL = input('ariga-url')
  }
  if (input('cloud-url')) {
    opts.cloudURL = input('cloud-url')
  }
  if (input('config-path')) {
    opts.configPath = input('config-path')
  }
  if (input('project-env')) {
    warning('project-env is deprecated, use config-env instead')
    opts.configEnv = input('project-env')
  }
  if (input('config-env')) {
    opts.configEnv = input('config-env')
  }
  if (input('token')) {
    opts.token = input('token')
  }
  if (input('skip-check-for-update') == 'true') {
    opts.skipCheckForUpdate = true
  }
  return opts
}
