# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.
# syntax=docker/dockerfile:1

ARG ALPINE_VERSION="3.21"

FROM alpine:${ALPINE_VERSION}
COPY LICENSE README.md /
COPY --chmod=001 ./scripts/setup-atlas.sh /usr/local/bin/setup-atlas
ARG TARGETARCH=amd64
COPY --chmod=001 ./atlas-action-linux-${TARGETARCH} /usr/local/bin/atlas-action
RUN apk add --update --no-cache curl
RUN setup-atlas && rm -rf /root/.atlas /tmp/*
WORKDIR /root
VOLUME /root/.atlas
ENTRYPOINT ["/usr/local/bin/atlas-action"]
