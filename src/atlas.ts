import { info, warning } from '@actions/core'
import { downloadTool } from '@actions/tool-cache'
import { exec, getExecOutput } from '@actions/exec'
import { getWorkingDirectory, resolveGitBase } from './github'
import { exists } from '@actions/io/lib/io-util'
import { getDownloadURL } from './cloud'
import path from 'path'
import { Options } from './input'

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

export interface Result {
  Name: string
  Text: string
  Reports: Report[] | null
  Error: string
}

export interface Step {
  Name: string
  Text: string
  Error: string
  Result: Result | null
}

export interface Env {
  Driver: string
  Dir: string
}
export interface Summary {
  Files: FileReport[]
  Env: Env
  Steps: Step[] | null
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
  Code: string
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

export async function atlasArgs(opts: Options): Promise<string[]> {
  const args = ['migrate', 'lint', '--log', '{{ json . }}']
  if (opts.projectEnv) {
    args.push('--env', opts.projectEnv)
  }
  if (opts.dir) {
    args.push('--dir', `file://${opts.dir}`)
  }
  if (opts.devUrl) {
    args.push('--dev-url', opts.devUrl)
  }
  if (opts.dirFormat) {
    args.push('--dir-format', opts.dirFormat)
  }
  if (opts.latest && opts.latest > 0) {
    args.push('--latest', opts.latest.toString())
  }
  if (!opts.projectEnv) {
    const gitRoot = path.resolve(await getWorkingDirectory())
    if (gitRoot) {
      args.push('--git-dir', gitRoot)
    }
    if (!opts.latest) {
      args.push('--git-base', `origin/${await resolveGitBase(gitRoot)}`)
    }
  }
  return args
}

export async function runAtlas(
  bin: string,
  opts: Options
): Promise<AtlasResult> {
  const args = await atlasArgs(opts)
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
      if (opts.schemaInsights) {
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
