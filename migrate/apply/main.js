const childProcess = require('child_process');

const mainCommand = 'atlas-action';
const args = ['--action', 'migrate/apply'];

const spawnSyncReturns = childProcess.spawnSync(mainCommand, args, { stdio: 'inherit' });

const exitCode = spawnSyncReturns.status;

if (exitCode !== 0) {
    process.exit(exitCode);
}
