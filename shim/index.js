const childProcess = require("child_process");
const fs = require("fs");
const path = require("path");
const which = require("which");
const core = require("@actions/core");
const toolCache = require("@actions/tool-cache");

module.exports = async function run(action) {
  const binaryName = "atlas-action";
  // Check for local mode (for testing)
  if (process.env.ATLAS_ACTION_LOCAL == 1) {
    // In the local mode, the atlas-action binary is expected to be in the PATH
    core.info("Running in local mode");
  } else {
    // Build or download the binary if not in local mode
    const version = process.env.GITHUB_ACTION_REF || "master";
    core.info(`Using version ${version}`);
    const toolName = "atlas-action";
    // We only cache the binary between steps of a single run.
    const cacheVersion = `${version}-${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
    let toolPath = toolCache.find(toolName, cacheVersion);
    // Tool Path is the directory where the binary is located. If it is not found, download it.
    if (!toolPath || !fs.existsSync(path.join(toolPath, binaryName))) {
      const dest = path.join(process.cwd(), "atlas-action");
      // The action can be run in the same job multiple times.
      // And the cache in only updated after the job is done.
      if (fs.existsSync(dest)) {
        core.debug(`Using downloaded binary: ${dest}`);
      } else if (!version.startsWith("v")) {
        if ((await which("go", { nothrow: true })) == null) {
          core.info("Invalid version, pinning action requires actions/setup-go@v5 to build the binary");
          throw new Error(`Invalid version: ${version}`);
        }
        const actionPath = path.join(
          process.env.RUNNER_WORKSPACE,
          "..",
          "_actions",
          process.env.GITHUB_ACTION_REPOSITORY,
          process.env.GITHUB_ACTION_REF
        );
        // Build the binary locally
        const { status, error, stderr } = childProcess.spawnSync("make", [
          "-C",
          actionPath,
          "atlas-action",
          `COMMIT=${version.substring(0, 7)}`
        ]);
        if (status !== 0 || error) {
          core.debug(`env: ${JSON.stringify(process.env)}`);
          core.debug(`build-dir: ${actionPath}`);
          throw new Error(`Unable to build the binary: ${stderr}`);
        }
        fs.copyFileSync(path.join(actionPath, binaryName), dest);
        fs.chmodSync(dest, "700");
      } else {
        const url = `https://release.ariga.io/atlas-action/atlas-action-${version}`;
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
