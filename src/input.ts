import Dict = NodeJS.Dict

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
  return opts
}
