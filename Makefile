BUILD_DATE = $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION    = $(shell cat VERSION.txt)
COMMIT    ?= $(shell git rev-parse --short HEAD)
VER_NO_V   = $(shell echo $(VERSION) | sed 's/^v//')
VER_MAJOR  = $(shell echo $(VER_NO_V) | cut -d. -f1)
VER_MINOR  = $(shell echo $(VER_NO_V) | cut -d. -f2)
VER_PATCH  = $(shell echo $(VER_NO_V) | cut -d. -f3)
LDFLAGS    = "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

BINARY_NAME  = atlas-action
DOCKER_IMAGE = arigaio/atlas-action

.PHONY: install
install:
	go install -ldflags $(LDFLAGS) ./cmd/atlas-action

.PHONY: atlas-action
atlas-action:
	go build -o atlas-action -ldflags $(LDFLAGS) ./cmd/atlas-action

.PHONY: test
test:
	@OUTPUT=$$(./atlas-action --version); \
	echo "$$OUTPUT" | grep -iq "$(VERSION)" && echo "Version=$$OUTPUT" || (echo "unexpected output: $$OUTPUT, expected to include: $(VERSION)"; exit 1)

.PHONY: s3-upload
s3-upload:
	aws s3 cp ./atlas-action s3://release.ariga.io/atlas-action/$(BINARY_NAME)-$(VERSION); \
	aws s3 cp ./atlas-action s3://release.ariga.io/atlas-action/$(BINARY_NAME)-v$(VER_MAJOR);

.PHONY: docker-build
docker-build:
	docker build \
		--label org.opencontainers.image.revision=$(COMMIT) \
		--label org.opencontainers.image.created=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):v$(VER_MAJOR) .

.PHONY: docker-push
docker-push:
	docker push $(DOCKER_IMAGE):$(VERSION)
	docker push $(DOCKER_IMAGE):v$(VER_MAJOR)

.PHONY: docker
docker: docker-build docker-push

.PHONY: release
release:
	@LATEST_VERSION=$$(gh release view --json tagName -q '.tagName' || echo "none"); \
	if [ "$$LATEST_VERSION" = "$(VERSION)" ]; then \
		echo "Latest release is already $(VERSION). No action needed."; \
		if [ ! -z "$$GITHUB_OUTPUT" ]; then \
			echo "status=skipped" >> "$$GITHUB_OUTPUT"; \
		fi; \
	else \
		echo "Creating new release for version $(VERSION)"; \
		gh release create "$(VERSION)" \
			--notes-start-tag="$$LATEST_VERSION" --generate-notes; \
		echo "Updating major version tag to v$(VER_MAJOR)"; \
		git tag -fa "v$(VER_MAJOR)" -m "release: update v$(VER_MAJOR) tag"; \
		git push origin "v$(VER_MAJOR)" --force; \
		if [ ! -z "$$GITHUB_OUTPUT" ]; then \
			echo "status=created" >> "$$GITHUB_OUTPUT"; \
		fi; \
	fi

.PHONY: azure-devops
azure-devops:
	# Copy the built binary to the Azure DevOps task directory
	cp ./atlas-action ./.github/azure-devops/action/atlas-action
	cp ./shim/dist/azure/index.js ./.github/azure-devops/action/shim.js
	jq '.version.Major = $$major | .version.Minor = $$minor | .version.Patch = $$patch' \
	  --argjson major "$(VER_MAJOR)" --argjson minor "$(VER_MINOR)" --argjson patch "$(VER_PATCH)" \
	  ./.github/azure-devops/action/task.json > ./.github/azure-devops/action/task.json.tmp && \
	mv ./.github/azure-devops/action/task.json.tmp ./.github/azure-devops/action/task.json && \
	jq '.version = $$version' \
	  --arg version "$(VER_NO_V)" \
	  ./.github/azure-devops/vss-extension.json > ./.github/azure-devops/vss-extension.json.tmp && \
	mv ./.github/azure-devops/vss-extension.json.tmp ./.github/azure-devops/vss-extension.json
	# Create the VSIX package
	cd ./.github/azure-devops && tfx extension create --manifest-globs vss-extension.json
