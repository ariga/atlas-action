import Dict = NodeJS.Dict

export type Options = {
  atlasVersion: string
  dir: string
  dirFormat: string
  devUrl: string
  latest: number
  arigaToken?: string
  arigaURL?: string
  schemaInsights: boolean
}

export function OptionsFromEnv(env: Dict<String>): Options {
  const input = (name: string) =>
    (env[`INPUT_${name.replace(/ /g, '_').toUpperCase()}`] || '') as string
  let opts: Options = {
    atlasVersion: input('atlas-version'),
    dir: input('dir'),
    dirFormat: input('dir-format'),
    devUrl: input('dev-url'),
    schemaInsights: true,
    arigaToken: input('ariga-token'),
    arigaURL: input('ariga-url'),
    latest: 0
  }
  if (input('latest').length) {
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
    opts.arigaToken = input('ariga-url')
  }
  return opts
}
