import { getInput, info, warning } from '@actions/core'
import { downloadTool } from '@actions/tool-cache'
import { exec, getExecOutput } from '@actions/exec'
import { getWorkingDirectory, resolveGitBase } from './github'
import { exists } from '@actions/io/lib/io-util'
import { getDownloadURL } from './cloud'
import path from 'path'

// Remove Atlas update messages.
process.env.ATLAS_NO_UPDATE_NOTIFIER = '1'

export enum ExitCodes {
  Success = 0,
  Failure = 1,
  Error = 2
}

export interface AtlasResult {
  cloudURL?: string
  exitCode: ExitCodes
  raw: string
  summary?: Summary
}

export interface Summary {
  Files: FileReport[]
  Env: unknown
  Steps: unknown
  Schema: unknown | null
}

export interface FileReport {
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

export async function installAtlas(version: string): Promise<string> {
  const downloadURL = getDownloadURL(version).toString()
  info(`Downloading atlas, version: ${version}`)
  // Setting user-agent for downloadTool is currently not supported.
  const bin = await downloadTool(downloadURL)
  await exec(`chmod +x ${bin}`)
  const res = await getExecOutput(bin, ['version'], {
    failOnStdErr: false,
    ignoreReturnCode: true,
    silent: true
  })
  info(`Installed Atlas version:\n${res.stdout ?? res.stderr}`)
  return bin
}

export async function runAtlas(bin: string): Promise<AtlasResult> {
  const dir = getMigrationDir()
  const devURL = getInput('dev-url')
  const runLatest = Number(getInput('latest'))
  const dirFormat = getInput('dir-format')
  const schemaInsights = getInput('schema-insights')
  const gitRoot = path.resolve(await getWorkingDirectory())
  info(`Migrations Directory: ${dir}`)
  info(`Dev Database: ${devURL}`)
  info(`Git Root: ${gitRoot}`)
  info(`Latest Param: ${runLatest}`)
  info(`Dir Format: ${dirFormat}`)
  info(`Schema Insights: ${schemaInsights}`)

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
    '{{ json . }}',
    '--dir-format',
    dirFormat
  ]
  if (!isNaN(runLatest) && runLatest > 0) {
    args.push('--latest', runLatest.toString())
  } else {
    args.push('--git-base', `origin/${await resolveGitBase(gitRoot)}`)
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
      a.summary = JSON.parse(res.stdout)
      if (schemaInsights == 'true') {
        return a
      }
      if (a.summary?.Schema) {
        a.summary.Schema = null
        a.raw = JSON.stringify(a.summary)
      }
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
