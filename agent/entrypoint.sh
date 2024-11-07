#!/bin/sh -l

./atlas-agent --token "$CLOUD_TOKEN" &
sleep "$WAIT_TIME"
