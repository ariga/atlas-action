interface ProcessEnv {
  [key: string]: string | undefined
}

export const originalENV = { ...process.env }

// These are the default environment variables that are set by the action (see action.yml)
export const defaultEnv = {
  'INPUT_DIR-FORMAT': 'atlas',
  'INPUT_DEV-URL': 'sqlite://test?mode=memory&cache=shared&_fk=1',
  INPUT_LATEST: '0',
  'INPUT_ARIGA-URL': `https://ci.ariga.cloud`,
  'INPUT_SCHEMA-INSIGHTS': 'true',
  ATLASCI_USER_AGENT: 'test-atlasci-action'
}

export function createTestENV(override: Record<string, string>): ProcessEnv {
  return {
    ...process.env,
    ...defaultEnv,
    ...override
  }
}
