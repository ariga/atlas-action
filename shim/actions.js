// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

const childProcess = require("child_process");
const fs = require("fs");
const path = require("path");
const core = require("@actions/core");
const toolCache = require("@actions/tool-cache");

module.exports = async function run(action) {
  const binaryName = "atlas-action";
  // Check for local mode (for testing)
  if (process.env.ATLAS_ACTION_LOCAL == 1) {
    // In the local mode, the atlas-action binary is expected to be in the PATH
    core.info("Running in local mode");
  } else {
    // Download the binary if not in local mode
    const version = process.env.GITHUB_ACTION_REF || "master";
    if (!version.startsWith("v") && version !== "master") {
      throw new Error(`Invalid version: ${version}`);
    }
    core.info(`Using version ${version}`);
    const toolName = "atlas-action";
    // We only cache the binary between steps of a single run.
    const cacheVersion = `${version}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
    let toolPath = toolCache.find(toolName, cacheVersion);
    // Tool Path is the directory where the binary is located. If it is not found, download it.
    if (!toolPath || !fs.existsSync(path.join(toolPath, binaryName))) {
      const url = `https://release.ariga.io/atlas-action/atlas-action-${version}`;
      const dest = path.join(process.cwd(), "atlas-action");
      // The action can be run in the same job multiple times.
      // And the cache in only updated after the job is done.
      if (fs.existsSync(dest)) {
        core.debug(`Using downloaded binary: ${dest}`);
      } else {
        core.info(`Downloading atlas-action binary: ${url} to ${dest}`);
        await toolCache.downloadTool(url, dest);
        fs.chmodSync(dest, "700");
      }
      toolPath = await toolCache.cacheFile(dest, binaryName, toolName, cacheVersion);
    }
    core.addPath(toolPath);
  }
  const { status, error } = childProcess.spawnSync(binaryName, ["--action", action], {
    stdio: "inherit"
  });
  if (status !== 0 || error) {
    core.error(error);
    core.setFailed(`The process exited with code ${status}`);
    // Always exit with an error code to fail the action
    process.exit(status || 1);
  }
};
