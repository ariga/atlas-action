#!/bin/sh
# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

# If ATLAS_BIN_DIR is set, Atlas is installed into that directory so it can be
# cached between runs, and the directory is prepended to PATH for later steps.
set -eu
found="$(command -v atlas 2>/dev/null || true)"
if [ -n "$found" ] && [ "$found" != "${ATLAS_BIN_DIR:-}/atlas" ]; then
  echo "Found pre-installed atlas-cli"
  atlas version
elif [ -n "${ATLAS_BIN_DIR:-}" ]; then
  mkdir -p "$ATLAS_BIN_DIR"
  curl -sSf https://atlasgo.sh | sh -s -- -o "$ATLAS_BIN_DIR/atlas"
  export PATH="$ATLAS_BIN_DIR:$PATH"
  if [ -n "${TEAMCITY_VERSION:-}" ]; then
    echo "##teamcity[setParameter name='env.PATH' value='$PATH']"
  fi
else
  curl -sSf https://atlasgo.sh | sh
fi
if [ -n "${ATLAS_TOKEN:-}" ]; then
  atlas login --token "$ATLAS_TOKEN"
fi
