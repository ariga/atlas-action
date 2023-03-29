import { mkdtemp, rm } from 'fs/promises'
import { tmpdir } from 'os'
import path from 'path'
import * as github from '@actions/github'
import * as yaml from 'js-yaml'
import * as fs from 'fs'

interface ProcessEnv {
  [key: string]: string | undefined
}

const originalEnv = { ...process.env }

// These are the default environment variables that are set by the action (see action.yml)
// https://docs.github.com/en/actions/creating-actions/metadata-syntax-for-github-actions#example-specifying-inputs
export const defaultEnv = {
  'INPUT_SCHEMA-INSIGHTS': 'true',
  'INPUT_ATLAS-VERSION': defaultVersion()
}

export const testDefaults = {
  INPUT_DIR: 'migrations',
  'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
  INPUT_LATEST: '0',
  'INPUT_ARIGA-URL': `https://ci.ariga.test`,
  'INPUT_CLOUD-URL': `https://ci.ariga.test`
}

// These are mocks for the GitHub context variables.
// https://docs.github.com/en/actions/learn-github-actions/environment-variables
const gitEnv = {
  ATLASCI_USER_AGENT: 'test-atlasci-action',
  GITHUB_REF_NAME: 'test-branch-trigger',
  GITHUB_HEAD_REF: 'test-pr-trigger'
}

// The GitHub Context as passed by the action.
// https://docs.github.com/en/actions/learn-github-actions/contexts#github-context
export const originalContext = { ...github.context }

type CreateTestEnvOutput = Promise<{
  cleanup: () => Promise<void>
  env: ProcessEnv
}>

type CreateTestEnvInput = {
  override?: Record<string, string>
  eventName?: GithubEventName
}

export enum GithubEventName {
  PullRequest = 'pull_request',
  Push = 'push'
}

export async function createTestEnv(
  input?: CreateTestEnvInput
): CreateTestEnvOutput {
  const eventName = input?.eventName ?? GithubEventName.PullRequest
  const contextMock = {
    value: {
      eventName: 'pull_request',
      payload: {
        repository: {
          default_branch: 'master',
          html_url: 'https://github.com/ariga/atlas-action'
        }
      }
    }
  }
  if (eventName === GithubEventName.PullRequest) {
    contextMock.value.payload = {
      ...contextMock.value.payload,
      ...{
        pull_request: {
          html_url: 'https://github.com/ariga/atlas-action/pull/1'
        }
      }
    }
  }
  Object.defineProperty(github, 'context', contextMock)
  const base = await mkdtemp(`${tmpdir()}${path.sep}`)
  const summaryFile = `${tmpdir()}/summary.txt`
  fs.closeSync(fs.openSync(summaryFile, 'w'))
  return {
    env: {
      ...process.env,
      // The path to a temporary directory on the runner (must be defined)
      RUNNER_TEMP: base,
      GITHUB_STEP_SUMMARY: summaryFile,
      ...testDefaults,
      ...defaultEnv,
      ...gitEnv,
      ...input?.override
    },
    cleanup: async () => {
      // Remove the temporary directory
      await rm(base, { recursive: true })
      process.env = { ...originalEnv }
      Object.defineProperty(github, 'context', {
        value: originalContext
      })
    }
  }
}

export function defaultVersion(): string {
  const action = fs.readFileSync('./action.yml', { encoding: 'utf-8' })
  const data = yaml.load(action) as {
    inputs: { [key: string]: { default: string } }
  }
  return data.inputs['atlas-version'].default
}
