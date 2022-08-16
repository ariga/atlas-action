import { error, getInput, notice } from '@actions/core'
import { existsSync } from 'fs'
import { stat } from 'fs/promises'
import { simpleGit } from 'simple-git'
import { AtlasResult } from './atlas'

export async function getWorkingDirectory(): Promise<string> {
  /**
   * getWorkingDirectory sets the path for the git root.
   * if working-directory is not set, it will default to the repository base directory.
   * */
  const workingDirectory = getInput(`working-directory`)
  if (
    workingDirectory &&
    existsSync(workingDirectory) &&
    (await stat(workingDirectory)).isDirectory()
  ) {
    return workingDirectory
  }
  // GITHUB_WORKSPACE is the default working directory on the runner for steps.
  return process.env.GITHUB_WORKSPACE ?? process.cwd()
}

export async function resolveGitBase(gitRoot: string): Promise<string> {
  if (process.env.GITHUB_BASE_REF) {
    return process.env.GITHUB_BASE_REF
  }
  const git = simpleGit(gitRoot)
  const origin = await git.remote(['show', 'origin'])
  const re = /HEAD branch: (.+)/g
  if (!origin) {
    throw new Error(`Could not find remote origin`)
  }
  const matches = [...origin.matchAll(re)]
  if (!matches || matches.length === 0) {
    throw new Error(`Could not find HEAD branch in remote origin`)
  }
  const baseBranch = matches[0]?.[1]
  if (!baseBranch) {
    throw new Error(`Could not find HEAD branch in remote origin`)
  }
  return baseBranch
}

export function report(res: AtlasResult): void {
  for (const file of res?.summary?.Files ?? []) {
    if (file.Error) {
      error(file.Error, {
        file: file.Name,
        title: `Error in Migrations file`,
        startLine: 0
      })
      continue
    }
    file?.Reports?.map(report => {
      report.Diagnostics?.map(diagnostic => {
        notice(`${report.Text}: ${diagnostic.Text}`, {
          // Atm we don't take into account the line number.
          startLine: 0,
          file: file.Name,
          title: report.Text
        })
      })
    })
  }
  res.cloudURL && notice(`For full report visit: ${res.cloudURL}`)
}
