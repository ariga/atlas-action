const childProcess = require('child_process'); // Import the child_process module

const mainCommand = 'atlas-action';
const args = ['--action', 'migrate/apply']; // You can put the arguments in an array

const spawnSyncReturns = childProcess.spawnSync(mainCommand, args, { stdio: 'inherit' });
