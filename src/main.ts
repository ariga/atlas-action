import {
  AtlasResult,
  ExitCodes,
  getMigrationDir,
  installAtlas,
  runAtlas
} from './atlas'
import { getInput, info, setFailed } from '@actions/core'

import path from 'path'
import { getWorkingDirectory, report } from './github'
import { context } from '@actions/github'
import { LATEST_RELEASE, reportToCloud } from './cloud'

// Entry point for GitHub Action runner.
export async function run(): Promise<AtlasResult | void> {
  try {
    const bin = await installAtlas(LATEST_RELEASE)
    const dir = getMigrationDir()
    const devURL = getInput('dev-url')
    const runLatest = Number(getInput('latest'))
    const dirFormat = getInput('dir-format')
    const gitRoot = path.resolve(await getWorkingDirectory())
    info(`Migrations directory set to ${dir}`)
    info(`Dev Database set to ${devURL}`)
    info(`Git Root set to ${gitRoot}`)
    const res = await runAtlas({
      dir,
      devURL,
      gitRoot,
      runLatest,
      bin,
      dirFormat
    })
    const out = res.fileReports?.length
      ? JSON.stringify(res.fileReports, null, 2)
      : res.raw
    if (
      res.exitCode !== ExitCodes.Success &&
      (res.fileReports?.length ?? 0) === 0
    ) {
      setFailed(`Atlas failed with code ${res.exitCode}: ${out}`)
      return res
    }
    info(`Atlas output: ${out}`)
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
