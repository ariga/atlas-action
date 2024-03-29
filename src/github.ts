import { error, getInput, notice, summary } from '@actions/core'
import { existsSync } from 'fs'
import { stat } from 'fs/promises'
import { simpleGit } from 'simple-git'
import { Summary } from './atlas'
import * as github from '@actions/github'
import * as path from 'path'
import { Options, PullRequest } from './input'
import { Octokit } from '@octokit/rest'
import { CloudReport } from './cloud'

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

export function report(opts: Options, s?: Summary, cloudURL?: string): void {
  const dir = s?.Env?.Dir
  for (const file of s?.Files ?? []) {
    if (!dir) {
      throw new Error('run summary must contain migration dir')
    }
    const fp = path.join(dir, file.Name)
    let annotate = notice
    if (file.Error) {
      annotate = error
      if (!file.Reports?.length) {
        error(file.Error, {
          file: fp,
          startLine: 1 // temporarily
        })
        continue
      }
    }
    file?.Reports?.map(report => {
      report.Diagnostics?.map(diagnostic => {
        let msg = diagnostic.Text
        if (diagnostic.Code) {
          msg = `${msg} (${diagnostic.Code})\n\nDetails: https://atlasgo.io/lint/analyzers#${diagnostic.Code}`
        }
        annotate(msg, {
          startLine: line(file.Text, diagnostic.Pos),
          file: fp,
          title: report.Text
        })
      })
    })
  }
  if (cloudURL) {
    notice(`For full report visit: ${cloudURL}`)
  }
}

export function summarize(s: Summary, report?: CloudReport): void {
  summary.addHeading('Atlas Lint Report')
  summary.addRaw(`Analyzed <strong>${s.Env.Dir}</strong><br><br>`)
  summary.addEOL()
  const steps = s?.Steps || []

  interface cell {
    data: string
    header: boolean
    colspan?: string
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
    let status = 'success'
    if (reports.length && !step.Error) {
      status = 'warning'
    }
    const diags: string[] = []
    const err = step.Error || step.Result?.Error
    if (err) {
      status = 'error'
      diags.push(err)
    }
    for (const report of step.Result?.Reports || []) {
      for (const diag of report.Diagnostics || []) {
        diags.push(
          `${diag.Text} (<a href="https://atlasgo.io/lint/analyzers#${diag.Code}">${diag.Code}</a>)`
        )
      }
    }
    rows.push([icon(status), step.Name, step.Text, diags.join('\n\n')])
  }
  const cloudReports = report?.result?.createReport.cloudReports || []
  for (const cloudReport of cloudReports) {
    if (!cloudReport) {
      continue
    }
    const diags: string[] = []
    diags.push(cloudReport.text + '\n')
    for (const diag of cloudReport.diagnostics || []) {
      if (!diag) {
        continue
      }
      diags.push(
        `${diag.text} (<a href="https://atlasgo.io/lint/analyzers#${diag.code}">${diag.code}</a>)`
      )
    }
    rows.push([
      icon('special-warning-icon'),
      'Analyze Database Schema',
      cloudReports.length.toString() + ' reports were found in analysis',
      diags.join('\n\n')
    ])
  }
  if (report?.prettyErr) {
    rows.push([
      { header: false, data: icon('error') },
      {
        header: false,
        data: `Could not report to <a href="https://auth.ariga.cloud/signup">Atlas Cloud</a>: ${report.prettyErr}`,
        colspan: '3'
      }
    ])
  } else if (!report?.result?.createReport.url) {
    rows.push([
      { header: false, data: icon('special-warning-icon') },
      {
        header: false,
        data: `Connect your project to <a href="https://auth.ariga.cloud/signup">Atlas Cloud</a> to get more safety checks`,
        colspan: '3'
      }
    ])
  }
  summary.addTable(rows)
}

function icon(n: string): string {
  return `<div align="center"><img src="https://release.ariga.io/images/assets/${n}.svg" /></div>`
}

interface Comment {
  id?: number
  body?: string
}

export async function comment(
  client: Octokit,
  pr: PullRequest,
  body: string,
  dir: string
): Promise<Comment | undefined> {
  const found = await findComment(client, pr, dir)
  const payload = await upsertComment(client, pr, found, body, dir)
  return {
    id: payload?.id,
    body: payload?.body
  }
}

async function upsertComment(
  client: Octokit,
  pr: PullRequest,
  comment: Comment | undefined,
  body: string,
  dir: string
): Promise<Comment | undefined> {
  body += `\n\n${commentSig(dir)}`
  if (!comment?.id) {
    const payload = await client.rest.issues.createComment({
      ...pr,
      body
    })
    return {
      id: payload.data.id
    }
  }
  const payload = await client.rest.issues.updateComment({
    comment_id: comment.id,
    ...pr,
    body
  })
  return {
    id: payload.data.id,
    body: payload.data.body
  }
}

async function findComment(
  client: Octokit,
  pr: PullRequest,
  dir: string
): Promise<Comment | undefined> {
  for await (const { data: comments } of client.paginate.iterator(
    client.rest.issues.listComments,
    {
      owner: pr.owner,
      repo: pr.repo,
      issue_number: pr.issue_number
    }
  )) {
    for (const comm of comments) {
      if (comm.body?.includes(commentSig(dir))) {
        return comm
      }
    }
  }
}

function line(s: string, pos: number): number {
  const c = s.substring(0, pos).split('\n')
  return c ? c.length : 1
}

function commentSig(dir: string): string {
  return `<!-- generated by ariga/atlas-action for ${dir} -->`
}
