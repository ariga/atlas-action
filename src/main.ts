import { AtlasResult, ExitCodes, installAtlas, runAtlas } from './atlas'
import { getInput, info, setFailed, summary } from '@actions/core'
import { report, summarize } from './github'
import { context } from '@actions/github'
import { reportToCloud } from './cloud'

// Entry point for GitHub Action runner.
export async function run(): Promise<AtlasResult | void> {
  try {
    const bin = await installAtlas(getInput('atlas-version'))
    const res = await runAtlas(bin)
    const out = res.summary ? JSON.stringify(res.summary, null, 2) : res.raw
    info(`\nAtlas output:\n${out}`)
    info(`Event type: ${context.eventName}`)
    const payload = await reportToCloud(res)
    if (payload) {
      res.cloudURL = payload.createReport.url
    }
    report(res)
    if (res.summary) {
      summarize(res.summary)
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

run()
