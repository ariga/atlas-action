const childProcess = require("child_process");
const fs = require("fs");
const path = require("path");

// The action input uses spaces (e.g., "schema plan approve") instead of slashes
// due to limitations in Azure DevOps task.json's visibleRule field,
// which does not handle '/' or quoted strings well.
//
// We converting the space-separated action string to the slash-separated
// format expected by the atlas-action binary (e.g., "schema/plan/approve").
const action = (process.env.INPUT_ACTION || "").replaceAll(" ", "/").toLowerCase();
if (!action) {
  throw new Error("Missing required input: action.");
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
