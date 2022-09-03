import { error, getInput, notice, summary } from '@actions/core'
import { existsSync } from 'fs'
import { stat } from 'fs/promises'
import { simpleGit } from 'simple-git'
import { AtlasResult, getMigrationDir, Summary } from './atlas'
import * as github from '@actions/github'
import * as path from 'path'

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
  if (github?.context?.payload?.repository?.default_branch) {
    return github.context.payload.repository.default_branch
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
    const fp = path.join(getMigrationDir(), file.Name)
    let annotate = notice
    if (file.Error) {
      annotate = error
      if (!file.Reports?.length) {
        error(file.Error, {
          file: fp,
          startLine: 1 // temporarily
        })
      }
      continue
    }
    file?.Reports?.map(report => {
      report.Diagnostics?.map(diagnostic => {
        let msg = diagnostic.Text
        if (diagnostic.Code) {
          msg = `${msg} (${diagnostic.Code})\n\nDetails: https://atlasgo.io/lint/analyzers#${diagnostic.Code}`
        }
        annotate(msg, {
          startLine: 1,
          file: fp,
          title: report.Text
        })
      })
    })
  }
  res.cloudURL && notice(`For full report visit: ${res.cloudURL}`)
}

export function summarize(s: Summary): void {
  summary.addHeading('Atlas Lint Report')
  summary.addEOL()
  const steps = s?.Steps || []
  interface cell {
    data: string
    header: boolean
  }
  type row = (cell | string)[]
  const rows: row[] = [
    [
      { header: true, data: 'Status' },
      { header: true, data: 'Step' },
      { header: true, data: 'Result' },
      { header: true, data: 'Diagnostics' }
    ]
  ]
  for (const step of steps) {
    const reports = step.Result?.Reports || []
    let emoj = '🟢'
    if (reports.length && !step.Error) {
      emoj = '🟡'
    }
    if (step.Error || step.Result?.Error) {
      emoj = '🔴'
    }
    const diags: string[] = []
    for (const report of step.Result?.Reports || []) {
      for (const diag of report.Diagnostics || []) {
        diags.push(
          `${diag.Text} (<a href="https://atlasgo.io/lint/analyzers#${diag.Code}">${diag.Code}</a>)`
        )
      }
    }
    rows.push([emoj, step.Name, step.Text, diags.join('\n\n')])
  }
  summary.addTable(rows)
}
