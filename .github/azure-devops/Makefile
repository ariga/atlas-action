VER_NO_V   = $(shell echo $(VERSION) | sed 's/^v//')
VER_MAJOR  = $(shell echo $(VER_NO_V) | cut -d. -f1)
VER_MINOR  = $(shell echo $(VER_NO_V) | cut -d. -f2)
VER_PATCH  = $(shell echo $(VER_NO_V) | cut -d. -f3)

prepare-binary:
	cp ../../atlas-action ./action/atlas-action

prepare-shim:
	cp ../../shim/dist/azure/index.js ./action/shim.js

version:
	jq '.version.Major = $$major | .version.Minor = $$minor | .version.Patch = $$patch' \
	  --argjson major "$(VER_MAJOR)" --argjson minor "$(VER_MINOR)" --argjson patch "$(VER_PATCH)" \
	  ./action/task.json > ./action/task.json.tmp
	jq '.version = $$version' --arg version "$(VER_NO_V)" ./vss-extension.json > ./vss-extension.json.tmp
	mv ./action/task.json.tmp ./action/task.json
	mv ./vss-extension.json.tmp ./vss-extension.json

.PHONY: vsix
vsix: prepare-binary prepare-shim version
	tfx extension create --manifest-globs vss-extension.json

.PHONY: clean
clean:
	rm -f ./action/atlas-action ./action/shim.js ./action/task.json.tmp ./vss-extension.json.tmp
	rm -f *.vsix
