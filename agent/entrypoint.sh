#!/bin/sh -l

echo "Running Atlas Agent"
# print all the files in the current directory
# Debugging: Print current directory and list files
echo "Current Directory: $(pwd)"
ls -l

/atlas-agent --token "$CLOUD_TOKEN" &
sleep "$WAIT_TIME"
