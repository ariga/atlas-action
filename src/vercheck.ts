import { HttpClient } from '@actions/http-client'

export interface Vercheck {
  Version?: string
  Summary?: string
  Link?: string
  Advisory?: string
}

export async function vercheck(cur: string): Promise<Vercheck> {
  const c = new HttpClient('atlas-action')
  const resp = await c.get(`https://vercheck.ariga.io/atlas-action/${cur}`)
  if (resp.message.statusCode != 200) {
    throw new Error(`vercheck failed: ${resp.message.statusMessage}`)
  }
  const payload = await resp.readBody()
  const parsed = JSON.parse(payload)
  const output: Vercheck = {}
  if (parsed.latest != null) {
    output.Version = parsed.latest.Version
    output.Link = parsed.latest.Link
    output.Summary = parsed.latest.Summary
  }
  if (parsed.advisory != null) {
    output.Advisory = parsed.advisory.Text
  }
  return output
}
