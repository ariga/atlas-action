import { Options, OptionsFromEnv, PullReqFromContext } from '../src/input'
import { expect } from '@jest/globals'
import { defaultEnv, defaultVersion } from './env'
import { atlasArgs } from '../src/atlas'
import * as fs from 'fs/promises'
import path from 'path'

describe('input', () => {
  test('defaults', () => {
    const options = OptionsFromEnv(defaultEnv)
    let expected: Options = {
      atlasVersion: defaultVersion(),
      schemaInsights: true
    }
    expect(options).toEqual(expected)
  })
  test('all args set', () => {
    let options = OptionsFromEnv({
      'INPUT_ATLAS-VERSION': 'v0.1.2',
      'INPUT_SCHEMA-INSIGHTS': 'false',
      INPUT_DIR: 'dir',
      'INPUT_DIR-FORMAT': 'atlas',
      'INPUT_DEV-URL': 'dev-url',
      'INPUT_ARIGA-TOKEN': 'ariga-token',
      'INPUT_ARIGA-URL': 'ariga-url',
      INPUT_LATEST: '3',
      'INPUT_PROJECT-ENV': 'env',
      'INPUT_SKIP-CHECK-FOR-UPDATE': 'true'
    })
    let expected: Options = {
      atlasVersion: 'v0.1.2',
      schemaInsights: false,
      dir: 'dir',
      dirFormat: 'atlas',
      devUrl: 'dev-url',
      arigaToken: 'ariga-token',
      arigaURL: 'ariga-url',
      latest: 3,
      projectEnv: 'env',
      skipCheckForUpdate: true
    }
    expect(options).toEqual(expected)
  })
})

describe('atlas args', () => {
  test('without env', async () => {
    const opts = OptionsFromEnv({
      ...defaultEnv,
      INPUT_DIR: 'dir',
      'INPUT_DIR-FORMAT': 'atlas',
      'INPUT_DEV-URL': 'dev-url'
    })
    let args = await atlasArgs(opts)
    const re: RegExp = new RegExp(
      'migrate lint --log {{ json . }} --dir file://dir --dev-url dev-url --dir-format atlas --git-dir .*/atlas-action --git-base origin/master'
    )
    expect(args.join(' ')).toMatch(re)
  })
  test('env set', async () => {
    let opts = OptionsFromEnv({
      ...defaultEnv,
      'INPUT_PROJECT-ENV': 'test'
    })
    let args = await atlasArgs(opts)
    expect(args.join(' ')).toEqual('migrate lint --log {{ json . }} --env test')
  })
})

describe('ctx', () => {
  test('empty', function () {
    // @ts-ignore (context gets populated when running in an action)
    const pr = PullReqFromContext({})
    expect(pr).toBeUndefined()
  })
  test('pr', async function () {
    const ctx = path.join('__tests__', 'testdata', 'pr-context.json')
    const data = JSON.parse((await fs.readFile(ctx)).toString())
    const pr = PullReqFromContext(data)
    expect(pr).toEqual({
      issue_number: 30,
      owner: 'ariga',
      repo: 'atlas-action'
    })
  })
})
