const childProcess = require('child_process');
const fs = require('fs');
const path = require('path');
const core = require('@actions/core');
const toolCache = require('@actions/tool-cache');
const semver = require("semver");

module.exports = async function run(action) {
    let isLocalMode = false;
    let version = "v1";

    // Check for local mode (for testing)
    if (!(process.env.GITHUB_ACTION_REPOSITORY && process.env.GITHUB_ACTION_REPOSITORY.length > 0)) {
        isLocalMode = true;
        core.info('Running in local mode')
    }

    // Check for version number
    if (process.env.GITHUB_ACTION_REF) {
        if (process.env.GITHUB_ACTION_REF.startsWith("v")) {
            version = process.env.GITHUB_ACTION_REF;
        } else if (process.env.GITHUB_ACTION_REF !== "master") {
            throw new Error(`Invalid version: ${process.env.GITHUB_ACTION_REF}`)
        }
    }


    core.info(`Using version ${version}`)

    // Download the binary if not in local mode
    if (!isLocalMode) {
        // We only cache the binary between steps of a single run.
        const cacheVersion = `${semver.coerce(version).version}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
        const url = `https://release.ariga.io/atlas-action/atlas-action-${version}`;
        let toolPath = toolCache.find('atlas-action', cacheVersion);
        if (!toolPath) {
            core.info(`Downloading atlas-action binary: ${url}`)
            const downloadDest = path.join(process.cwd(), 'atlas-action');
            // check if the binary is already in 'atlas-action' file
            if (!fs.existsSync(downloadDest)) {
                await toolCache.downloadTool(url, downloadDest);
                fs.chmodSync(downloadDest, '700');
            }
            toolPath = await toolCache.cacheFile(downloadDest, 'atlas-action', 'atlas-action', cacheVersion);
        }
        core.addPath(toolPath);
    }

    const args = ['--action', action];
    const res = childProcess.spawnSync("atlas-action", args, {stdio: 'inherit'});

    const exitCode = res.status;
    if (exitCode !== 0 || res.error) {
        core.error(res.error)
        core.setFailed(`The process exited with code ${exitCode}`);
        process.exit(exitCode);
    }
}
