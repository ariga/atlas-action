import { AtlasResult, ExitCodes, installAtlas, runAtlas } from './atlas'
import { info, setFailed } from '@actions/core'
import { report } from './github'
import { context } from '@actions/github'
import { LATEST_RELEASE, reportToCloud } from './cloud'

// Entry point for GitHub Action runner.
export async function run(): Promise<AtlasResult | void> {
  try {
    const bin = await installAtlas(LATEST_RELEASE)
    const res = await runAtlas(bin)
    const out = res.summary ? JSON.stringify(res.summary, null, 2) : res.raw
    if (
      res.exitCode !== ExitCodes.Success &&
      (res.summary?.Files?.length ?? 0) === 0
    ) {
      setFailed(`Atlas failed with code ${res.exitCode}: ${out}`)
      return res
    }
    info(`\nAtlas output:\n${out}`)
    info(`Event type: ${context.eventName}`)
    const payload = await reportToCloud(res)
    if (payload) {
      res.cloudURL = payload.createReport.url
    }
    report(res)
    return res
  } catch (error) {
    setFailed((error as Error)?.message ?? error)
  }
}

run()
