const childProcess = require('child_process');

// Log all environment variables
console.log('Environment Variables:');
for (const [key, value] of Object.entries(process.env)) {
    console.log(`${key}: ${value}`);
}

const mainCommand = 'atlas-action';
const args = ['--action', 'migrate/apply'];

const spawnSyncReturns = childProcess.spawnSync(mainCommand, args, { stdio: 'inherit' });

// Get the exit code
const exitCode = spawnSyncReturns.status;

// Or if this is a script, you can set the process exit code
if (exitCode !== 0) {
    process.exit(exitCode);
}
