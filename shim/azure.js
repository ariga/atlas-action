// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

const childProcess = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

// The action input uses spaces (e.g., "schema plan approve") instead of slashes
// due to limitations in Azure DevOps task.json's visibleRule field,
// which does not handle '/' or quoted strings well.
//
// We convert the space-separated action string to the slash-separated
// format expected by the atlas-action binary (e.g., "schema/plan/approve").
const action = (process.env.INPUT_ACTION || "").trim().replaceAll(" ", "/").toLowerCase();
if (!action) {
  throw new Error("Missing required input: action.");
}

if (action === "setup") {
  const token = process.env.INPUT_CLOUD_TOKEN;
  const baseVersion = process.env.INPUT_ATLAS_VERSION || "latest";
  const flavor = (process.env.INPUT_FLAVOR || "").trim();
  // Mirror setup-atlas behavior: prepend "extended-" when a flavor is specified.
  const version = flavor ? `${flavor}-${baseVersion}` : baseVersion;

  // Azure Pipelines Cache@2 can only cache paths inside Pipeline.Workspace.
  // ~/.atlas lives outside it, so we mirror it into a workspace-relative
  // directory that Cache@2 manages, and copy back here on every run.
  const pipelineWorkspace = process.env.PIPELINE_WORKSPACE || process.env.AGENT_BUILDDIRECTORY;
  if (!pipelineWorkspace) {
    console.error("##[error]PIPELINE_WORKSPACE is not set. Is this running on an Azure Pipelines agent?");
    process.exit(1);
  }
  const cacheDir = path.join(pipelineWorkspace, ".atlas");
  const homeAtlas = path.join(os.homedir(), ".atlas");

  // Restore cached grant from workspace into ~/.atlas (populated by Cache@2 before this step)
  fs.mkdirSync(homeAtlas, { recursive: true });
  if (fs.existsSync(cacheDir)) {
    childProcess.spawnSync("cp", ["-a", cacheDir + "/.", homeAtlas + "/"], { stdio: "inherit" });
  }

  // Install Atlas CLI
  console.log(`##[section]Installing Atlas CLI (version: ${version})`);
  const install = childProcess.spawnSync(
    "sh", ["-c", `curl -sSf https://atlasgo.sh | ATLAS_VERSION="${version}" CI=true sh`],
    { stdio: "inherit" }
  );
  if (install.status !== 0) {
    console.error("##[error]Failed to install Atlas CLI.");
    process.exit(install.status || 1);
  }

  // Fetch/refresh the offline grant
  if (token) {
    console.log("##[section]Authenticating to Atlas Cloud (grant-only)");
    const login = childProcess.spawnSync(
      "atlas", ["login", "--token", token, "--grant-only"],
      { stdio: "inherit" }
    );
    if (login.status !== 0) {
      console.error("##[error]Atlas login failed.");
      process.exit(login.status || 1);
    }
    // Expose the token for subsequent AtlasAction steps in the same job
    process.stdout.write(`##vso[task.setvariable variable=ATLAS_TOKEN;issecret=true;]${token}\n`);
  }
  process.exit(0);
}

const bin = path.join(__dirname, "atlas-action");
try {
  // Only change permission if execute is not set
  const stat = fs.statSync(bin);
  if ((stat.mode & 0o111) === 0) {
    fs.chmodSync(bin, stat.mode | 0o111);
  }
} catch (err) {
  console.error("##[error]OS currently is not supported.");
  process.exit(1);
}
const { status, error } = childProcess.spawnSync(bin, ["--action", action], {
  stdio: "inherit"
});
if (status !== 0 || error) {
  if (error) {
    console.log("##[error]" + error);
  }
  // Always exit with an error code to fail the action
  process.exit(status || 1);
}
