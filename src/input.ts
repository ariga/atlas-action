import Dict = NodeJS.Dict
import { Context } from '@actions/github/lib/context'

export type Options = {
  atlasVersion: string
  dir?: string
  dirFormat?: string
  devUrl?: string
  latest?: number
  arigaToken?: string
  arigaURL?: string
  projectEnv?: string
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
  if (input('dir-format')) {
    opts.dirFormat = input('dir-format')
  }
  if (input('dev-url')) {
    opts.devUrl = input('dev-url')
  }
  if (input('ariga-token')) {
    opts.arigaToken = input('ariga-token')
  }
  if (input('ariga-url')) {
    opts.arigaURL = input('ariga-url')
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
  if (input('ariga-token')) {
    opts.arigaToken = input('ariga-token')
  }
  if (input('ariga-url')) {
    opts.arigaURL = input('ariga-url')
  }
  if (input('project-env')) {
    opts.projectEnv = input('project-env')
  }
  if (input('token')) {
    opts.token = input('token')
  }
  if (input('skip-check-for-update') == 'true') {
    opts.skipCheckForUpdate = true
  }
  return opts
}
