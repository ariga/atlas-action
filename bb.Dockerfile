# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.
# syntax=docker/dockerfile:1

ARG ALPINE_VERSION="3.21"

FROM alpine:${ALPINE_VERSION} AS setup
RUN apk add --update --no-cache bash curl
ARG PIPE_OWNER=arigaio
RUN echo -e "#!/usr/bin/env bash\n" \
  "\n" \
  "export HOME=\$BITBUCKET_PIPE_SHARED_STORAGE_DIR/${PIPE_OWNER}\n" \
  "mkdir -p \$HOME\n" \
  "curl -sSf https://atlasgo.sh | sh -s -- -o \$BITBUCKET_PIPE_STORAGE_DIR/atlas\n" \
  "if [ \"\$ATLAS_TOKEN\" ]; then\n" \
  "  \$BITBUCKET_PIPE_STORAGE_DIR/atlas login --token \$ATLAS_TOKEN\n" \
  "fi\n" > /pipe.sh \
  && chmod +x /pipe.sh
ENTRYPOINT ["/pipe.sh"]

FROM alpine:${ALPINE_VERSION} AS action
RUN apk add --update --no-cache bash
ARG ATLAS_ACTION
ARG PIPE_OWNER=arigaio
ARG PIPE_SETUP=setup-atlas
RUN echo -e "#!/usr/bin/env bash\n" \
  "\n" \
  "export HOME=\$BITBUCKET_PIPE_SHARED_STORAGE_DIR/${PIPE_OWNER}\n" \
  "mkdir -p \$HOME\n" \
  "export PATH=\$BITBUCKET_PIPE_SHARED_STORAGE_DIR/${PIPE_OWNER}/${PIPE_SETUP}:\$PATH\n" \
  "/atlas-action --action=${ATLAS_ACTION}\n" > /pipe.sh \
  && chmod +x /pipe.sh
COPY --chmod=001 ./atlas-action /atlas-action
ENTRYPOINT ["/pipe.sh"]
