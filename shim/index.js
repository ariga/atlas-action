const childProcess = require('child_process');
const fs = require('fs');
const path = require('path');
const core = require('@actions/core');
const toolcache = require('@actions/tool-cache');

module.exports = async function run(action) {
    // core.info all env vars
    core.info('Environment variables:')
    for (const key in process.env) {
        core.info(`${key}: ${process.env[key]}`)
    }

    let isLocalMode = false;
    let version = "v1";

    // Check for local mode (for testing)
    if (!(process.env.GITHUB_ACTION_REPOSITORY && process.env.GITHUB_ACTION_REPOSITORY.length > 0)) {
        isLocalMode = true;
        core.info('Running in local mode')
    }

    // Check for version number
    if (process.env.GITHUB_ACTION_REF) {
        version = process.env.GITHUB_ACTION_REF;
    }
    core.info(`Using version ${version}`)

    let toolPath;
    // Download the binary if not in local mode
    if (!isLocalMode) {
        const url = `https://release.ariga.io/atlas-action/atlas-action-${version}`;
        core.info('Downloading atlas-action binary: ', url)
        toolPath = await toolcache.downloadTool(url, 'atlas-action');
        fs.chmodSync(toolPath, '700'); // Assuming the binary is directly within toolPath
    }

    // Add tool to path if not in local mode
    let mainCommand = 'atlas-action';
    if (toolPath) {
        mainCommand = path.join(process.cwd(), mainCommand);
    }
    const args = ['--action', action];

    const res = childProcess.spawnSync(mainCommand, args, {stdio: 'inherit'});

    const exitCode = res.status;
    if (exitCode !== 0 || res.error) {
        core.error(res.error)
        core.setFailed(`The process exited with code ${exitCode}`);
        process.exit(exitCode);
    }
}