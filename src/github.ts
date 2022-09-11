import { error, getInput, notice, summary } from '@actions/core'
import { existsSync } from 'fs'
import { stat } from 'fs/promises'
import { simpleGit } from 'simple-git'
import { Summary } from './atlas'
import * as github from '@actions/github'
import * as path from 'path'
import { Options, PullRequest } from './input'

const commentSig =
  'Reviewed by <a href="https://atlasgo.io/integrations/github-actions">atlas-action</a>'

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
          startLine: 1,
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

export function summarize(s: Summary, cloudURL?: string): void {
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
    let status = 'success'
    if (reports.length && !step.Error) {
      status = 'warning'
    }
    if (step.Error || step.Result?.Error) {
      status = 'error'
    }
    const diags: string[] = []
    for (const report of step.Result?.Reports || []) {
      for (const diag of report.Diagnostics || []) {
        diags.push(
          `${diag.Text} (<a href="https://atlasgo.io/lint/analyzers#${diag.Code}">${diag.Code}</a>)`
        )
      }
    }
    rows.push([icon(status), step.Name, step.Text, diags.join('\n\n')])
  }
  summary.addTable(rows)
  if (cloudURL) {
    summary.addLink('Full Report', cloudURL)
  }
}

function icon(n: string): string {
  return `<div align="center"><img src="https://release.ariga.io/images/assets/${n}.svg" /></div>`
}

interface Comment {
  id?: number
  body?: string
}

export async function comment(
  token: string,
  pr: PullRequest,
  body: string
): Promise<Comment | undefined> {
  const found = await findComment(token, pr)
  const payload = await upsertComment(token, pr, found, body)
  return {
    id: payload?.id,
    body: payload?.body
  }
}

async function upsertComment(
  token: string,
  pr: PullRequest,
  comment: Comment | undefined,
  body: string
): Promise<Comment | undefined> {
  const octokit = github.getOctokit(token)
  body += `\n\n${commentSig}`
  if (!comment?.id) {
    const payload = await octokit.rest.issues.createComment({
      ...pr,
      body
    })
    return {
      id: payload.data.id
    }
  }
  const payload = await octokit.rest.issues.updateComment({
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
  token: string,
  pr: PullRequest
): Promise<Comment | undefined> {
  const octokit = github.getOctokit(token)
  for await (const { data: comments } of octokit.paginate.iterator(
    octokit.rest.issues.listComments,
    {
      owner: pr.owner,
      repo: pr.repo,
      issue_number: pr.issue_number
    }
  )) {
    for (const comm of comments) {
      if (comm.body?.includes('Reviewed by atlas-action')) {
        return comm
      }
    }
  }
}
