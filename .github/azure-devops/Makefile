# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

VER_NO_V   = $(shell echo $(VERSION) | sed 's/^v//')
VER_MAJOR  = $(shell echo $(VER_NO_V) | cut -d. -f1)
VER_MINOR  = $(shell echo $(VER_NO_V) | cut -d. -f2)
VER_PATCH  = $(shell echo $(VER_NO_V) | cut -d. -f3)

prepare:
	cp ../../atlas-action ./action/atlas-action
	cp ../../shim/dist/azure/index.js ./action/shim.js
	cp ./images/atlas.png ./action/icon.png

version:
	jq '.version.Major = $$major | .version.Minor = $$minor | .version.Patch = $$patch' \
	  --argjson major "$(VER_MAJOR)" --argjson minor "$(VER_MINOR)" --argjson patch "$(VER_PATCH)" \
	  ./action/task.json > ./action/task.json.tmp
	jq '.version = $$version' --arg version "$(VER_NO_V)" \
	  ./vss-extension.json > ./vss-extension.json.tmp
	mv ./action/task.json.tmp ./action/task.json
	mv ./vss-extension.json.tmp ./vss-extension.json

version-dev: version
	jq '.name = "AtlasActionDev" | .friendlyName = "Atlas Action Dev" | .id = $$id' \
	  --arg id "43908d25-6448-44e3-accf-49df010ca313" \
	  ./action/task.json > ./action/task.json.tmp
	jq '.name = "Atlas Action Dev" | .id = "atlas-action-dev"' \
	  ./vss-extension.json > ./vss-extension.json.tmp
	mv ./action/task.json.tmp ./action/task.json
	mv ./vss-extension.json.tmp ./vss-extension.json

.PHONY: vsix
vsix: prepare
	tfx extension create --manifest-globs vss-extension.json

.PHONY: clean
clean:
	rm -f ./action/atlas-action ./action/shim.js ./action/task.json.tmp ./vss-extension.json.tmp
	rm -f *.vsix
