const childProcess = require('child_process');
const fs = require('fs');
const path = require('path');
const core = require('@actions/core');
const toolCache = require('@actions/tool-cache');
const semver = require("semver");

module.exports = async function run(action) {
    const binaryName = "atlas-action"
    // Check for local mode (for testing)
    const isLocalMode = !(process.env.GITHUB_ACTION_REPOSITORY && process.env.GITHUB_ACTION_REPOSITORY.length > 0);
    if (isLocalMode) {
        // In the local mode, the atlas-action binary is expected to be in the PATH
        core.info('Running in local mode')
    } else {
        // Download the binary if not in local mode
        let version = "v1";
        // Check for version number
        if (process.env.GITHUB_ACTION_REF) {
            if (process.env.GITHUB_ACTION_REF.startsWith("v")) {
                version = process.env.GITHUB_ACTION_REF;
            } else if (process.env.GITHUB_ACTION_REF !== "master") {
                throw new Error(`Invalid version: ${process.env.GITHUB_ACTION_REF}`)
            }
        }
        core.info(`Using version ${version}`)
        const toolName = "atlas-action"
        // We only cache the binary between steps of a single run.
        const cacheVersion = `${semver.coerce(version).version}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
        let toolPath = toolCache.find(toolName, cacheVersion);
        // Tool Path is the directory where the binary is located. If it is not found, download it.
        if (!toolPath || !fs.existsSync(path.join(toolPath, binaryName))) {
            const url = `https://release.ariga.io/atlas-action/atlas-action-${version}`;
            const dest = path.join(process.cwd(), 'atlas-action');
            // The action can be run in the same job multiple times.
            // And the cache in only updated after the job is done.
            if (fs.existsSync(dest)) {
                core.debug(`Using downloaded binary: ${dest}`)
            } else {
                core.info(`Downloading atlas-action binary: ${url} to ${dest}`)
                await toolCache.downloadTool(url, dest);
                fs.chmodSync(dest, '700');
            }
            toolPath = await toolCache.cacheFile(dest, binaryName, toolName, cacheVersion);
        }
        core.addPath(toolPath);
    }
    const { status, error } = childProcess.spawnSync(binaryName, ['--action', action], {
        stdio: 'inherit',
    });
    if (status !== 0 || error) {
        core.error(error)
        core.setFailed(`The process exited with code ${status}`);
        process.exit(status);
    }
}
