# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

FROM arigaio/atlas:latest-alpine
ARG ATLAS_ACTION
ENV ATLAS_ACTION=$ATLAS_ACTION
COPY --chmod=001 ./atlas-action /atlas-action
ENTRYPOINT ["/atlas-action"]
