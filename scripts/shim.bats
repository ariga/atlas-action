#!/usr/bin/env bats
# Test suite for shim.sh

bats_require_minimum_version 1.5.0

# Path to the script under test
SHIM_SCRIPT="$BATS_TEST_DIRNAME/shim.sh"

setup() {
  # Save original environment
  export _ORIG_PATH="$PATH"

  # Clear CI platform variables (alphabetically sorted)
  unset AGENT_TOOLSDIRECTORY
  unset ATLAS_ACTION
  unset ATLAS_ACTION_LOCAL
  unset ATLAS_ACTION_VERSION
  unset CI_PROJECT_DIR
  unset GITHUB_ACTION_REF
  unset GITHUB_ACTIONS
  unset GITHUB_PATH
  unset GITLAB_CI
  unset RUNNER_TOOL_CACHE
  unset TEAMCITY_PATH_PREFIX
  unset TEAMCITY_VERSION
  unset TF_BUILD

  # Create temp directory for tests
  TEST_TMPDIR="$(mktemp -d)"
  export TEST_TMPDIR
  GITHUB_PATH="$TEST_TMPDIR/github_path"
  export GITHUB_PATH
  touch "$GITHUB_PATH"
}

teardown() {
  # Restore environment
  export PATH="$_ORIG_PATH"

  # Clean up temp directory
  [ -d "$TEST_TMPDIR" ] && rm -rf "$TEST_TMPDIR"
}

# Helper to create a mock atlas-action binary
create_mock_binary() {
  mkdir -p "$TEST_TMPDIR/bin"
  cat > "$TEST_TMPDIR/bin/atlas-action" << 'EOF'
#!/bin/sh
echo "mock-atlas-action"
echo "args: $@"
EOF
  chmod +x "$TEST_TMPDIR/bin/atlas-action"
  export PATH="$TEST_TMPDIR/bin:$PATH"
}

# =============================================================================
# CI Platform Detection Tests (via log output format)
# =============================================================================

@test "detects GitHub Actions platform" {
  export ATLAS_ACTION_LOCAL="1"
  export GITHUB_ACTIONS="true"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"::notice::Detected CI platform: github"* ]]
}

@test "detects GitLab CI platform" {
  export ATLAS_ACTION_LOCAL="1"
  export GITLAB_CI="true"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  # GitLab output includes ANSI color codes
  [[ "$output" == *"Detected CI platform: gitlab"* ]]
}

@test "detects TeamCity platform" {
  export ATLAS_ACTION_LOCAL="1"
  export TEAMCITY_VERSION="2023.05"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"##teamcity[message text='Detected CI platform: teamcity'"* ]]
}

@test "detects Azure DevOps platform" {
  export ATLAS_ACTION_LOCAL="1"
  export TF_BUILD="True"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"##[debug]Detected CI platform: azure"* ]]
}

@test "defaults to unknown platform" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[INFO] Detected CI platform: unknown"* ]]
}

@test "GitHub Actions takes precedence over GitLab CI" {
  export ATLAS_ACTION_LOCAL="1"
  export GITHUB_ACTIONS="true"
  export GITLAB_CI="true"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"::notice::Detected CI platform: github"* ]]
}

# =============================================================================
# Log Format Tests (via local mode execution)
# =============================================================================

@test "log_notice outputs Azure format" {
  export ATLAS_ACTION_LOCAL="1"
  export TF_BUILD="True"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"##[debug]"* ]]
}

@test "log_notice outputs GitHub format" {
  export ATLAS_ACTION_LOCAL="1"
  export GITHUB_ACTIONS="true"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"::notice::"* ]]
}

@test "log_notice outputs GitLab format" {
  export ATLAS_ACTION_LOCAL="1"
  export GITLAB_CI="true"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[INFO]"* ]]
}

@test "log_notice outputs TeamCity format" {
  export ATLAS_ACTION_LOCAL="1"
  export TEAMCITY_VERSION="2023.05"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"##teamcity[message text="* ]]
}

@test "log_notice outputs default format for unknown platform" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[INFO]"* ]]
}

# =============================================================================
# Version Tests
# =============================================================================

@test "uses ATLAS_ACTION_VERSION when set" {
  command -v curl >/dev/null || skip "curl not installed"
  export ATLAS_ACTION_VERSION="v2.0.0"
  export GITHUB_ACTIONS="true"
  # Check the version is logged correctly (will fail to download v2.0.0)
  run ! "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using version v2.0.0"* ]]
}

@test "uses GITHUB_ACTION_REF on GitHub" {
  command -v curl >/dev/null || skip "curl not installed"
  export GITHUB_ACTION_REF="v1.2.3"
  export GITHUB_ACTIONS="true"
  # Check the version is logged correctly (will fail to download v1.2.3)
  run ! "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using version v1.2.3"* ]]
}

@test "defaults to v1 on GitHub without GITHUB_ACTION_REF" {
  command -v curl >/dev/null || skip "curl not installed"
  export GITHUB_ACTIONS="true"
  unset GITHUB_ACTION_REF
  # Check the version v1 is logged (may download or use cache)
  run "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using version v1"* ]]
}

@test "defaults to v1 on non-GitHub platforms" {
  command -v curl >/dev/null || skip "curl not installed"
  export GITLAB_CI="true"
  unset GITHUB_ACTION_REF
  # Check if the version v1 is used (may download or use cache)
  run "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using version v1"* ]]
}

@test "ATLAS_ACTION_VERSION takes precedence over GITHUB_ACTION_REF" {
  command -v curl >/dev/null || skip "curl not installed"
  export ATLAS_ACTION_VERSION="v3.0.0"
  export GITHUB_ACTION_REF="v1.2.3"
  export GITHUB_ACTIONS="true"
  # Check v3.0.0 is used, not v1.2.3 (will fail to download v3.0.0)
  run ! "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using version v3.0.0"* ]]
  [[ "$output" != *"Using version v1.2.3"* ]]
}

@test "rejects version without v prefix" {
  export ATLAS_ACTION_VERSION="1.0.0"
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 1 ]
  [[ "$output" == *"Invalid version"* ]]
  [[ "$output" == *"must start with 'v'"* ]]
}

@test "accepts version with v prefix" {
  export ATLAS_ACTION_LOCAL="1"
  export ATLAS_ACTION_VERSION="v1.0.0"
  create_mock_binary
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" != *"Invalid version"* ]]
}

# =============================================================================
# Platform Detection Tests
# =============================================================================

@test "detects platform as linux on Linux" {
  command -v curl >/dev/null || skip "curl not installed"
  export GITHUB_ACTIONS="true"
  # Check the platform detection log message
  run "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Detected platform: linux-"* ]]
}

# =============================================================================
# Entry Point Tests
# =============================================================================

@test "script requires action argument" {
  run "$SHIM_SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"Usage:"* ]]
}

@test "script accepts action as first argument" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT" "migrate/lint"
  [ "$status" -eq 0 ]
  [[ "$output" == *"mock-atlas-action"* ]]
  [[ "$output" == *"--action"* ]]
  [[ "$output" == *"migrate/lint"* ]]
}

@test "script accepts ATLAS_ACTION environment variable" {
  export ATLAS_ACTION="schema/apply"
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"mock-atlas-action"* ]]
  [[ "$output" == *"--action"* ]]
  [[ "$output" == *"schema/apply"* ]]
}

@test "argument takes precedence over ATLAS_ACTION env var" {
  export ATLAS_ACTION="schema/apply"
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT" "migrate/lint"
  [ "$status" -eq 0 ]
  [[ "$output" == *"migrate/lint"* ]]
  [[ "$output" != *"schema/apply"* ]]
}

# =============================================================================
# Local Mode Tests
# =============================================================================

@test "local mode skips download" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary
  run "$SHIM_SCRIPT" "migrate/apply"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Running in local mode"* ]]
  [[ "$output" == *"mock-atlas-action"* ]]
  [[ "$output" != *"Downloading"* ]]
}

@test "local mode executes atlas-action with correct arguments" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary

  run "$SHIM_SCRIPT" "migrate/lint"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Running in local mode"* ]]
  [[ "$output" == *"mock-atlas-action"* ]]
  [[ "$output" == *"args: --action migrate/lint"* ]]
  [[ "$output" != *"Downloading"* ]]
}

# =============================================================================
# Download Mode Tests (without network - just verify behavior)
# =============================================================================

@test "download mode attempts to create cache directory" {
  command -v curl >/dev/null || skip "curl not installed"
  export ATLAS_ACTION_VERSION="v1.0.0"
  # Don't set ATLAS_ACTION_LOCAL, so it will try to download
  # This will fail with 404 (22) since v1.0.0 doesn't exist
  run ! "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Downloading"* ]]
}

@test "download URL includes version and platform" {
  command -v curl >/dev/null || skip "curl not installed"
  export ATLAS_ACTION_VERSION="v1.2.3"
  # This will fail with 404 (22) since v1.2.3 doesn't exist
  run ! "$SHIM_SCRIPT" "test"
  [[ "$output" == *"atlas-action-linux-"* ]]
  [[ "$output" == *"v1.2.3"* ]]
}

# =============================================================================
# PATH Modification Tests (GitHub Actions)
# =============================================================================

@test "GitHub Actions appends to GITHUB_PATH" {
  command -v curl >/dev/null || skip "curl not installed"

  export ATLAS_ACTION_VERSION="v1"
  export GITHUB_ACTIONS="true"
  export RUNNER_TOOL_CACHE="$TEST_TMPDIR/cache"

  # Create GITHUB_PATH file
  GITHUB_PATH="$TEST_TMPDIR/github_path_test"
  export GITHUB_PATH
  touch "$GITHUB_PATH"

  # Run the script (will download the binary)
  run "$SHIM_SCRIPT" "test"

  # Verify GITHUB_PATH file was updated with the cache directory
  [ -f "$GITHUB_PATH" ]
  path_content=$(cat "$GITHUB_PATH")
  [[ "$path_content" == *"$TEST_TMPDIR/cache/atlas-action"* ]]
}

# =============================================================================
# Error Handling Tests
# =============================================================================

@test "fails gracefully when action binary not found in local mode" {
  export ATLAS_ACTION_LOCAL="1"
  # Don't add atlas-action to PATH - exec will fail with 127 (command not found)
  run -127 "$SHIM_SCRIPT" "test"
  [[ "$output" == *"local mode"* ]] || [[ "$output" == *"atlas-action"* ]]
}

@test "error output uses correct format for platform" {
  export ATLAS_ACTION_VERSION="invalid"
  export GITHUB_ACTIONS="true"
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 1 ]
  [[ "$output" == *"::error::"* ]]
}

@test "error output uses TeamCity format" {
  export ATLAS_ACTION_VERSION="invalid"
  export TEAMCITY_VERSION="2023.05"
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 1 ]
  [[ "$output" == *"##teamcity[message text="*"status='ERROR'"* ]]
}

@test "error output uses Azure format" {
  export ATLAS_ACTION_VERSION="invalid"
  export TF_BUILD="True"
  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 1 ]
  [[ "$output" == *"##[error]"* ]]
}

# =============================================================================
# Binary Caching Tests
# =============================================================================

@test "downloads binary and caches it for subsequent runs" {
  # Skip if curl is not available
  command -v curl >/dev/null || skip "curl not installed"

  export ATLAS_ACTION_VERSION="v1"
  export GITHUB_ACTIONS="true"
  export RUNNER_TOOL_CACHE="$TEST_TMPDIR/cache"

  platform="linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  cache_dir="$TEST_TMPDIR/cache/atlas-action/v1/$platform"

  # Verify cache directory doesn't exist yet
  [ ! -d "$cache_dir" ]

  # First run - should download the binary
  # The action itself may fail (no atlas CLI) but download should succeed
  run "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Downloading"* ]]
  [[ "$output" != *"Using cached binary"* ]]

  # Verify binary was downloaded to cache
  [ -f "$cache_dir/atlas-action" ]
  [ -x "$cache_dir/atlas-action" ]

  # Verify the binary is a real executable (not empty or corrupt)
  file_size=$(wc -c < "$cache_dir/atlas-action")
  [ "$file_size" -gt 1000000 ]  # Binary should be > 1MB

  # Second run - should use cached binary
  run "$SHIM_SCRIPT" "test"
  [[ "$output" == *"Using cached binary"* ]]
  [[ "$output" != *"Downloading"* ]]
}

@test "uses cached binary if available and working" {
  export ATLAS_ACTION_VERSION="v1.0.0"
  export GITHUB_ACTIONS="true"
  export RUNNER_TOOL_CACHE="$TEST_TMPDIR/cache"

  # Pre-create a cached binary
  platform="linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
  cache_dir="$TEST_TMPDIR/cache/atlas-action/v1.0.0/$platform"
  mkdir -p "$cache_dir"
  cat > "$cache_dir/atlas-action" << 'EOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "atlas-action v1.0.0"
  exit 0
fi
echo "cached-binary-executed"
echo "args: $@"
exit 0
EOF
  chmod 700 "$cache_dir/atlas-action"

  run "$SHIM_SCRIPT" "test"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Using cached binary"* ]]
  [[ "$output" == *"cached-binary-executed"* ]]
}

# =============================================================================
# Integration Tests
# =============================================================================

@test "full execution flow in local mode" {
  export ATLAS_ACTION_LOCAL="1"
  export GITHUB_ACTIONS="true"
  create_mock_binary

  run "$SHIM_SCRIPT" "schema/push"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Running in local mode"* ]]
  [[ "$output" == *"mock-atlas-action"* ]]
  [[ "$output" == *"schema/push"* ]]
  [[ "$output" != *"Downloading"* ]]
}

@test "handles special characters in action name" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary

  run "$SHIM_SCRIPT" "migrate/lint"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Running in local mode"* ]]
  [[ "$output" == *"migrate/lint"* ]]
  [[ "$output" != *"Downloading"* ]]
}

@test "propagates exit code from atlas-action binary" {
  export ATLAS_ACTION_LOCAL="1"

  # Create a mock binary that exits with code 42
  mkdir -p "$TEST_TMPDIR/bin"
  cat > "$TEST_TMPDIR/bin/atlas-action" << 'EOF'
#!/bin/sh
echo "mock-atlas-action exiting with code 42"
exit 42
EOF
  chmod +x "$TEST_TMPDIR/bin/atlas-action"
  export PATH="$TEST_TMPDIR/bin:$PATH"

  run -42 "$SHIM_SCRIPT" "migrate/lint"
  [[ "$output" == *"mock-atlas-action exiting with code 42"* ]]
}

@test "propagates success exit code from atlas-action binary" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary

  run "$SHIM_SCRIPT" "migrate/lint"
  [ "$status" -eq 0 ]
}

# =============================================================================
# Pipeline Execution Tests (simulates TeamCity curl | sh -s --)
# =============================================================================

@test "works when piped through sh (like curl | sh)" {
  export ATLAS_ACTION_LOCAL="1"
  create_mock_binary

  run sh -c "cat '$SHIM_SCRIPT' | sh -s -- migrate/lint"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Running in local mode"* ]]
  [[ "$output" == *"mock-atlas-action"* ]]
}

@test "propagates exit code from atlas-action when piped" {
  export ATLAS_ACTION_LOCAL="1"

  # Create a mock binary that exits with code 5
  mkdir -p "$TEST_TMPDIR/bin"
  cat > "$TEST_TMPDIR/bin/atlas-action" << 'EOF'
#!/bin/sh
echo "failing with exit code 5"
exit 5
EOF
  chmod +x "$TEST_TMPDIR/bin/atlas-action"
  export PATH="$TEST_TMPDIR/bin:$PATH"

  run sh -c "cat '$SHIM_SCRIPT' | sh -s -- migrate/lint"
  [ "$status" -eq 5 ]
  [[ "$output" == *"failing with exit code 5"* ]]
}

