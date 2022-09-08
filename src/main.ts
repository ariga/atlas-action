import { AtlasResult, ExitCodes, installAtlas, runAtlas } from './atlas'
import { info, setFailed, summary } from '@actions/core'
import { report, summarize } from './github'
import { context } from '@actions/github'
import { reportToCloud } from './cloud'
import { Options, OptionsFromEnv } from './input'

// Entry point for GitHub Action runner.
export async function run(opts: Options): Promise<AtlasResult | void> {
  try {
    const bin = await installAtlas(opts.atlasVersion)
    const res = await runAtlas(bin, opts)
    const out = res.summary ? JSON.stringify(res.summary, null, 2) : res.raw
    info(`\nAtlas output:\n${out}`)
    info(`Event type: ${context.eventName}`)
    const payload = await reportToCloud(opts, res)
    if (payload) {
      res.cloudURL = payload.createReport.url
    }
    report(opts, res.summary, res.cloudURL)
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

const opts: Options = OptionsFromEnv(process.env)
run(opts)
