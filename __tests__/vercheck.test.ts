import { vercheck, Vercheck } from '../src/vercheck'
import nock from 'nock'
import * as http from '@actions/http-client'
import { expect } from '@jest/globals'

describe('vercheck', function () {
  afterEach(() => {
    nock.cleanAll()
  })
  const testCase = async (v: string, payload: string, exp: Vercheck) => {
    test(v, async () => {
      const scope = nock('https://vercheck.ariga.io')
        .get(`/atlas-action/${v}`)
        .reply(http.HttpCodes.OK, payload)

      const actual = await vercheck(v)
      scope.done()
      expect(actual).toEqual(exp)
    })
  }

  testCase('v0', '{}', {})
  testCase(
    'v0.1.0',
    '{"latest":{"Version":"v0.2.0","Summary":"","Link":"https://github.com/ariga/atlas-action/releases/tag/v0.2.0"},"advisory":null}',
    {
      Link: 'https://github.com/ariga/atlas-action/releases/tag/v0.2.0',
      Version: 'v0.2.0',
      Summary: ''
    }
  )
})
