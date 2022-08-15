import { getInput, info, warning } from '@actions/core'
import { downloadTool } from '@actions/tool-cache'
import { exec, getExecOutput } from '@actions/exec'
import { resolveGitBase } from './github'
import { exists } from '@actions/io/lib/io-util'
import { getDownloadURL, LATEST_RELEASE } from './cloud'

interface RunAtlasParams {
  dir: string
  devURL: string
  gitRoot: string
  runLatest: number
  bin: string
  dirFormat: string
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
  const downloadURL = getDownloadURL(version).toString()
  info(`Downloading atlas, version: ${version}`)
  // Setting user-agent for downloadTool is currently not supported.
  const bin = await downloadTool(downloadURL)
  await exec(`chmod +x ${bin}`)
  return bin
}

export async function runAtlas({
  dir,
  devURL,
  gitRoot,
  runLatest,
  bin,
  dirFormat
}: RunAtlasParams): Promise<AtlasResult> {
  const args = [
    'migrate',
    'lint',
    '--dir',
    `file://${dir}`,
    '--dev-url',
    devURL,
    '--git-dir',
    gitRoot,
    '--log',
    '{{ json .Files }}',
    '--dir-format',
    dirFormat
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
  if (!dir || !exists(dir)) {
    throw new Error(`Migration directory ${dir} doesn't exist`)
  }
  return dir
}
