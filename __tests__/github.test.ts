import { resolveGitBase } from '../src/github'
import { simpleGit } from 'simple-git'
import { expect } from '@jest/globals'
import { createTestEnv, GithubEventName } from './env'
import * as github from '@actions/github'

jest.setTimeout(30000)
process.env.ATLASCI_USER_AGENT = 'test-atlasci-action'

describe('resolve git base', () => {
  let cleanupFn: () => Promise<void>, base: string

  beforeEach(async () => {
    const { cleanup, env } = await createTestEnv({
      override: {
        // Remove base ref since GitHub action sets it.
        GITHUB_BASE_REF: ''
      }
    })
    process.env = env
    if (!env.RUNNER_TEMP) {
      throw new Error('RUNNER_TEMP is not defined')
    }
    base = env.RUNNER_TEMP
    cleanupFn = cleanup
  })

  afterEach(async () => {
    await cleanupFn()
    process.env.GITHUB_BASE_REF = 'master'
  })

  test('branch mode - base is master', async () => {
    await simpleGit(base).init({ initialBranch: 'master' })
    await expect(resolveGitBase(base)).resolves.toBe('master')
  })

  test('branch mode - base is main', async () => {
    Object.defineProperty(github, 'context', { value: {} })
    process.env.GITHUB_BASE_REF = ''
    const remote = `https://github.com/actions/javascript-action.git`
    await simpleGit().clone(remote, base, ['--depth', '1'])
    await expect(resolveGitBase(base)).resolves.toBe('main')
  })

  test('pull request mode', async () => {
    process.env.GITHUB_BASE_REF = 'master'
    await expect(resolveGitBase(base)).resolves.toBe('master')
  })

  test('base from context', async () => {
    await createTestEnv({ eventName: GithubEventName.Push })
    process.env.GITHUB_BASE_REF = ''
    await expect(resolveGitBase(base)).resolves.toBe('master')
  })
})
