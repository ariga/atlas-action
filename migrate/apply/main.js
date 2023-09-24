const mainScript = `atlas-action --action migrate/apply`
const spawnSyncReturns = childProcess.spawnSync(mainScript, { stdio: 'inherit' })
