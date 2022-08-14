import { getInput, info, warning } from '@actions/core'
import { downloadTool } from '@actions/tool-cache'
import { exec, getExecOutput } from '@actions/exec'
import * as os from 'os'
import { resolveGitBase } from './github'
import { exists } from '@actions/io/lib/io-util'

const BASE_ADDRESS = 'https://release.ariga.io'
const S3_FOLDER = 'atlas'
export const LATEST_RELEASE = 'latest'
const LINUX_ARCH = 'linux-amd64'
const APPLE_ARCH = 'darwin-amd64'

interface RunAtlasParams {
  dir: string
  devDB: string
  gitRoot: string
  runLatest: number
  bin: string
}

export enum ExitCodes {
  Success = 0,
  Failure = 1,
  Error = 2
}

export interface AtlasResult {
  cloudURL?: string
  exitCode: ExitCodes
  raw: string
  fileReports?: FileReport[]
}

interface FileReport {
  Name: string
  Text: string
  Reports?: Report[]
  Error?: string
}

interface Report {
  Text: string
  Diagnostics?: Diagnostic[]
}

interface Diagnostic {
  Pos: number
  Text: string
}

export async function installAtlas(
  version: string = LATEST_RELEASE
): Promise<string> {
  const arch = os.platform() === 'darwin' ? APPLE_ARCH : LINUX_ARCH
  const downloadURL = new URL(
    `${BASE_ADDRESS}/${S3_FOLDER}/atlas-${arch}-${version}`
  ).toString()
  info(`Downloading atlas ${version}`)
  const bin = await downloadTool(
    downloadURL,
    undefined,
    undefined,
    getUserAgent()
  )
  await exec(`chmod +x ${bin}`)
  return bin
}

export async function runAtlas({
  dir,
  devDB,
  gitRoot,
  runLatest,
  bin
}: RunAtlasParams): Promise<AtlasResult> {
  const args = [
    'migrate',
    'lint',
    '--dir',
    `file://${dir}`,
    '--dev-url',
    devDB,
    '--git-dir',
    gitRoot,
    '--log',
    '{{ json .Files }}'
  ]
  if (!isNaN(runLatest) && runLatest > 0) {
    args.push('--latest', runLatest.toString())
  } else {
    args.push('--git-base', await resolveGitBase(gitRoot))
  }
  const res = await getExecOutput(bin, args, {
    failOnStdErr: false,
    ignoreReturnCode: true
  })
  const a: AtlasResult = {
    exitCode: res.exitCode,
    raw: res.stderr || res.stdout
  }
  if (res.stdout && res.stdout.length > 0) {
    try {
      a.fileReports = JSON.parse(res.stdout)
    } catch (e) {
      warning(`Failed to parse JSON output from Atlas CLI, ${e}: ${a.raw}`)
    }
  }
  return a
}

export function getUserAgent(): { 'User-Agent': string } {
  return {
    'User-Agent': process.env.ATLASCI_USER_AGENT ?? 'AtlasCI-Action'
  }
}

export function getMigrationDir(): string {
  const dir = getInput('dir')
  if (!exists(dir)) {
    throw new Error(`Migration directory ${dir} doesn't exist`)
  }
  return dir
}
