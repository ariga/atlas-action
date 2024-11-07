#!/bin/sh -l

set -e

echo "Running Atlas Agent"
/atlas-agent --token "$CLOUD_TOKEN" &
sleep "$WAIT_TIME"
