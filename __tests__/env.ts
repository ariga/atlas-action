import { mkdtemp } from 'fs/promises'
import { tmpdir } from 'os'
import path from 'path'

interface ProcessEnv {
  [key: string]: string | undefined
}

export const originalENV = { ...process.env }

// These are the default environment variables that are set by the action (see action.yml)
// https://docs.github.com/en/actions/creating-actions/metadata-syntax-for-github-actions#example-specifying-inputs
const defaultENV = {
  'INPUT_DIR-FORMAT': 'atlas',
  'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
  INPUT_LATEST: '0',
  'INPUT_ARIGA-URL': `https://ci.ariga.cloud`,
  'INPUT_SCHEMA-INSIGHTS': 'true'
}

// These are mocks for the GitHub context variables.
// https://docs.github.com/en/actions/learn-github-actions/environment-variables
const gitENV = {
  ATLASCI_USER_AGENT: 'test-atlasci-action',
  GITHUB_REF_NAME: 'test-branch-trigger',
  GITHUB_HEAD_REF: 'test-pr-trigger'
}

export async function createTestENV(
  override?: Record<string, string>
): Promise<ProcessEnv> {
  const base = await mkdtemp(`${tmpdir()}${path.sep}`)
  return {
    ...process.env,
    // The path to a temporary directory on the runner (must be defined)
    RUNNER_TEMP: base,
    ...defaultENV,
    ...gitENV,
    ...override
  }
}
