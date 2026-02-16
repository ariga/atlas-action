#!/bin/sh
# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

set -eu
if command -v atlas > /dev/null 2>&1; then
  echo "Found pre-installed atlas-cli"
  atlas version
else
  curl -sSf https://atlasgo.sh | sh
fi
if [ -n "${ATLAS_TOKEN:-}" ]; then
  atlas login --token "$ATLAS_TOKEN"
fi
