import { resolveGitBase } from '../src/github'
import { mkdtemp, rm } from 'fs/promises'
import { tmpdir } from 'os'
import { simpleGit } from 'simple-git'
import { expect } from '@jest/globals'
import path from 'path'

jest.setTimeout(30000)
process.env.ATLASCI_USER_AGENT = 'test-atlasci-action'

describe('resolve git base', () => {
  let base: string
  beforeEach(async () => {
    // Remove base ref since GitHub action sets it.
    process.env.GITHUB_BASE_REF = ''
    base = await mkdtemp(`${tmpdir()}${path.sep}`)
  })
  afterEach(() => {
    rm(base, { recursive: true })
    process.env.GITHUB_BASE_REF = 'master'
  })

  test('branch mode - base is master', async () => {
    const remote = `https://github.com/ariga/atlas.git`
    await simpleGit().clone(remote, base)
    await expect(resolveGitBase(base)).resolves.toBe('master')
  })

  test('branch mode - base is main', async () => {
    const remote = `https://github.com/actions/javascript-action.git`
    await simpleGit().clone(remote, base)
    await expect(resolveGitBase(base)).resolves.toBe('main')
  })

  test('pull request mode', async () => {
    process.env.GITHUB_BASE_REF = 'master'
    await expect(resolveGitBase(base)).resolves.toBe('master')
  })
})
