import { Options, OptionsFromEnv } from '../src/input'
import { expect } from '@jest/globals'
import { createTestEnv, defaultVersion } from './env'

describe('input', () => {
  test('defaults', async () => {
    let prep = await createTestEnv()
    let options = OptionsFromEnv(prep.env)
    let expected: Options = {
      atlasVersion: defaultVersion(),
      dir: 'migrations',
      dirFormat: 'atlas',
      devUrl: 'sqlite://test?mode=memory&cache=shared&_fk=1',
      schemaInsights: true,
      latest: 0,
      arigaToken: 'https://ci.ariga.test',
      arigaURL: 'https://ci.ariga.test'
    }
    expect(options).toEqual(expected)
  })
})
