#!/usr/bin/env bash
# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

# Usage: s3-put-once.sh <bucket> <key> <file> <content-type>
#
# Publish <file> as s3://<bucket>/<key>, but never replace an existing object.
# Used for the versioned and per-commit release keys, which the bucket policy
# makes write-once: PutObject is only accepted with "If-None-Match: *".
#
#   - The key does not exist: upload it.
#   - The key exists with the same content (a re-run): log it and succeed.
#   - The key exists with different content: fail. Nothing is overwritten.
#   - Anything else (permissions, network, missing bucket, ...): fail.
#
# `aws s3 cp --no-overwrite` is not used on purpose: it sends If-None-Match but
# exits 0 without a message on a 412, even if the existing content differs.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <bucket> <key> <file> <content-type>" >&2
  exit 2
fi
bucket=$1
key=$2
file=$3
content_type=$4
uri="s3://${bucket}/${key}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# s3api does not guess the Content-Type, so it is always passed explicitly.
if aws s3api put-object \
  --bucket "$bucket" --key "$key" --body "$file" \
  --content-type "$content_type" --if-none-match '*' \
  >/dev/null 2>"$tmp/put.err"; then
  echo "uploaded ${uri}"
  exit 0
fi

# HTTP 412: the key exists. Any other failure is fatal.
if ! grep -q 'PreconditionFailed' "$tmp/put.err"; then
  cat "$tmp/put.err" >&2
  echo "::error::failed to upload ${uri}"
  exit 1
fi

# Download to a file instead of piping into cmp, so that a failed download
# (permissions, network) is not mistaken for a content mismatch.
if ! aws s3 cp "$uri" "$tmp/existing" --only-show-errors; then
  echo "::error::${uri} already exists, but it could not be downloaded to verify its content"
  exit 1
fi
if cmp -s "$tmp/existing" "$file"; then
  echo "${uri} is already published with identical content"
  exit 0
fi
echo "::error::${uri} already exists with different content from ${file}. Published release keys are immutable and are never overwritten: bump VERSION.txt to publish different content"
exit 1
