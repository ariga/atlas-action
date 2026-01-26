#!/bin/sh
# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

set -eu

BINARY_NAME="atlas-action"

# Detect CI platform
if [ -n "${GITHUB_ACTIONS:-}" ]; then
  CI_PLATFORM="github"
elif [ -n "${GITLAB_CI:-}" ]; then
  CI_PLATFORM="gitlab"
elif [ -n "${TEAMCITY_VERSION:-}" ]; then
  CI_PLATFORM="teamcity"
elif [ -n "${TF_BUILD:-}" ]; then
  CI_PLATFORM="azure"
else
  CI_PLATFORM="unknown"
fi

# Logging functions
log_notice() {
  case "$CI_PLATFORM" in
    azure)    echo "##[debug]$1" ;;
    github)   echo "::notice::$1" ;;
    gitlab)   printf '\033[36m[INFO]\033[0m %s\n' "$1" ;;
    teamcity) echo "##teamcity[message text='$1' status='NORMAL']" ;;
    *)        echo "[INFO] $1" ;;
  esac
}

log_error() {
  case "$CI_PLATFORM" in
    azure)    echo "##[error]$1" ;;
    github)   echo "::error::$1" ;;
    gitlab)   printf '\033[31m[ERROR]\033[0m %s\n' "$1" >&2 ;;
    teamcity) echo "##teamcity[message text='$1' status='ERROR']" ;;
    *)        echo "[ERROR] $1" >&2 ;;
  esac
}

# Get action version (ATLAS_ACTION_VERSION takes precedence)
get_version() {
  if [ -n "${ATLAS_ACTION_VERSION:-}" ]; then
    echo "$ATLAS_ACTION_VERSION"
  elif [ "$CI_PLATFORM" = "github" ]; then
    echo "${GITHUB_ACTION_REF:-v1}"
  else
    echo "v1"
  fi
}

# Get tool cache directory
get_tool_cache() {
  case "$CI_PLATFORM" in
    azure)    echo "${AGENT_TOOLSDIRECTORY:-/tmp}" ;;
    github)   echo "${RUNNER_TOOL_CACHE:-/tmp}" ;;
    gitlab)   echo "${CI_PROJECT_DIR:-.}/.cache" ;;
    teamcity) echo "${AGENT_TOOLSDIRECTORY:-/tmp}" ;;
    *)              echo "/tmp" ;;
  esac
}

# Add directory to PATH
add_to_path() {
  export PATH="$1:$PATH"
  case "$CI_PLATFORM" in
    azure)
      echo "##vso[task.prependpath]$1"
      ;;
    github)
      echo "$1" >> "$GITHUB_PATH"
      ;;
    teamcity)
      new_path="$1"
      [ -n "${TEAMCITY_PATH_PREFIX:-}" ] && new_path="$1:$TEAMCITY_PATH_PREFIX"
      echo "##teamcity[setParameter name='env.TEAMCITY_PATH_PREFIX' value='$new_path']"
      ;;
  esac
}

# Detect OS and architecture
get_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$arch" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)  arch="arm64" ;;
    *) log_error "Unsupported architecture: $arch"; exit 1 ;;
  esac

  case "$os" in
    linux|darwin) ;;
    *) log_error "Unsupported OS: $os"; exit 1 ;;
  esac

  echo "${os}-${arch}"
}

run() {
  log_notice "Detected CI platform: $CI_PLATFORM"

  # Local mode: binary expected in PATH
  if [ "${ATLAS_ACTION_LOCAL:-}" = "1" ]; then
    log_notice "Running in local mode"
    exec "$BINARY_NAME" --action "$1"
  fi

  # Download binary
  version="$(get_version)"
  case "$version" in
    v*) ;;
    *) log_error "Invalid version: $version (must start with 'v')"; exit 1 ;;
  esac
  log_notice "Using version $version"

  platform="$(get_platform)"
  log_notice "Detected platform: $platform"

  tool_path="$(get_tool_cache)/${BINARY_NAME}/${version}/${platform}"
  dest="${tool_path}/${BINARY_NAME}"

  # Download binary if not already cached and working
  if bin_version=$("$dest" --version 2>/dev/null); then
    log_notice "Using cached binary: $bin_version"
  else
    url="https://release.ariga.io/atlas-action/atlas-action-${platform}-${version}"
    mkdir -p "$tool_path"
    log_notice "Downloading: $url"
    curl -sSfL "$url" -o "$dest"
    chmod 700 "$dest"
  fi

  add_to_path "$tool_path"
  exec "$BINARY_NAME" --action "$1"
}

# Entry point
action="${1:-${ATLAS_ACTION:-}}"
[ -z "$action" ] && { log_error "Usage: $0 <action>"; exit 1; }
run "$action"
