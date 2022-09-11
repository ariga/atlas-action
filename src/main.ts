import { AtlasResult, ExitCodes, installAtlas, runAtlas } from './atlas'
import { info, setFailed, summary } from '@actions/core'
import { comment, report, summarize } from './github'
import { context } from '@actions/github'
import { reportToCloud } from './cloud'
import { OptionsFromEnv, PullReqFromContext, RunInput } from './input'

// Entry point for GitHub Action runner.
export async function run(input: RunInput): Promise<AtlasResult | void> {
  try {
    const bin = await installAtlas(input.opts.atlasVersion)
    const res = await runAtlas(bin, input.opts)
    const out = res.summary ? JSON.stringify(res.summary, null, 2) : res.raw
    info(`\nAtlas output:\n${out}`)
    info(`Event type: ${context.eventName}`)
    const payload = await reportToCloud(input.opts, res)
    if (payload) {
      res.cloudURL = payload.createReport.url
    }
    report(input.opts, res.summary, res.cloudURL)
    if (res.summary) {
      summarize(res.summary)
      const body = commentBody(res.cloudURL)
      if (input.opts.token && input.pr) {
        await comment(input.opts.token, input.pr, body)
      }
      await summary.write()
    }
    if (res.exitCode !== ExitCodes.Success) {
      setFailed(`Atlas failed with code ${res.exitCode}: ${out}`)
    }
    return res
  } catch (error) {
    setFailed((error as Error)?.message ?? error)
  }
}

function commentBody(cloudURL?: string): string {
  let s = summary.stringify()
  if (cloudURL) {
    s += `<a href="${cloudURL}">Full Report on Ariga Cloud</a>`
  }
  s += '<hr/>'
  return s
}

run({
  opts: OptionsFromEnv(process.env),
  pr: PullReqFromContext(context)
})
