// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

// postjobexecution hook for AtlasAction.
// Only runs when action == "setup". Copies ~/.atlas back into
// $(Pipeline.Workspace)/.atlas so that a Cache@2 step with
// path: $(Pipeline.Workspace)/.atlas can persist the grant.

const childProcess = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const action = (process.env.INPUT_ACTION || "").trim().replaceAll(" ", "/").toLowerCase();
if (action !== "setup") {
  process.exit(0);
}

const pipelineWorkspace = process.env.PIPELINE_WORKSPACE || process.env.AGENT_BUILDDIRECTORY;
if (!pipelineWorkspace) {
  console.log("post-shim: PIPELINE_WORKSPACE and AGENT_BUILDDIRECTORY are both unset — skipping cache copy.");
  process.exit(0);
}

const cacheDir = path.join(pipelineWorkspace, ".atlas");
const homeAtlas = path.join(os.homedir(), ".atlas");

if (!fs.existsSync(homeAtlas)) {
  console.log("post-shim: ~/.atlas does not exist — nothing to save.");
  process.exit(0);
}

fs.mkdirSync(cacheDir, { recursive: true });
console.log(`post-shim: copying ${homeAtlas} → ${cacheDir}`);
const { status } = childProcess.spawnSync(
  "cp", ["-a", homeAtlas + "/.", cacheDir + "/"],
  { stdio: "inherit" }
);
if (status === 0) {
  console.log("post-shim: grant cache saved successfully.");
} else {
  console.log(`post-shim: cp exited with status ${status}.`);
}
process.exit(status || 0);
