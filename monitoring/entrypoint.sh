#!/bin/sh -l

set -e

# Base command
cmd="/atlas-agent push-snapshot --token \"$CLOUD_TOKEN\" --url \"$URL\""

# Conditionally add flags
if [ -n "$EXT_ID" ]; then
  cmd="$cmd --ext-id \"$EXT_ID\""
fi

if [ -n "$SCHEMAS" ]; then
  cmd="$cmd --schemas \"$SCHEMAS\""
fi

if [ -n "$EXCLUDE" ]; then
  cmd="$cmd --exclude \"$EXCLUDE\""
fi

snapshot_url=$(eval "$cmd")

echo "$snapshot_url"

# Write snapshot URL to output of GitHub Action
echo "snapshot-url=$snapshot_url" >> $GITHUB_OUTPUT
