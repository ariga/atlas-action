# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

BUILD_DATE = $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION    = $(shell cat VERSION.txt)
COMMIT    ?= $(shell git rev-parse --short HEAD)
MAJOR_VER  = $(shell echo "$(VERSION)" | cut -d. -f1)
LDFLAGS    = "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"

BINARY_NAME  = atlas-action
DOCKER_IMAGE = arigaio/atlas-action
ATLAS_BIN_LINUX_AMD64 = $(BINARY_NAME)-linux-amd64
ATLAS_BIN_LINUX_ARM64 = $(BINARY_NAME)-linux-arm64
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: install
install:
	go install -ldflags $(LDFLAGS) ./cmd/atlas-action

.PHONY: atlas-action
atlas-action: $(ATLAS_BIN_LINUX_AMD64)
	cp $(ATLAS_BIN_LINUX_AMD64) $(BINARY_NAME)

.PHONY: test
test:
	@OUTPUT=$$(./atlas-action --version); \
	echo "$$OUTPUT" | grep -iq "$(VERSION)" && echo "Version=$$OUTPUT" || (echo "unexpected output: $$OUTPUT, expected to include: $(VERSION)"; exit 1)

.PHONY: s3-upload
s3-upload: $(ATLAS_BIN_LINUX_AMD64) $(ATLAS_BIN_LINUX_ARM64)
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) s3://release.ariga.io/atlas-action/$(BINARY_NAME)-$(VERSION); \
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) s3://release.ariga.io/atlas-action/$(BINARY_NAME)-$(MAJOR_VER); \
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) s3://release.ariga.io/atlas-action/$(ATLAS_BIN_LINUX_AMD64)-$(VERSION); \
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) s3://release.ariga.io/atlas-action/$(ATLAS_BIN_LINUX_AMD64)-$(MAJOR_VER); \
	aws s3 cp ./$(ATLAS_BIN_LINUX_ARM64) s3://release.ariga.io/atlas-action/$(ATLAS_BIN_LINUX_ARM64)-$(VERSION); \
	aws s3 cp ./$(ATLAS_BIN_LINUX_ARM64) s3://release.ariga.io/atlas-action/$(ATLAS_BIN_LINUX_ARM64)-$(MAJOR_VER);

.PHONY: docker
docker: $(ATLAS_BIN_LINUX_AMD64) $(ATLAS_BIN_LINUX_ARM64)
	docker buildx build \
		--platform $(DOCKER_PLATFORMS) \
		--label org.opencontainers.image.revision=$(COMMIT) \
		--label org.opencontainers.image.created=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):$(MAJOR_VER) \
		--push .

$(BINARY_NAME)-linux-%:
	GOOS=linux GOARCH=$* go build -o $@ -ldflags $(LDFLAGS) ./cmd/atlas-action

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
		echo "Updating major version tag to $(MAJOR_VER)"; \
		git tag -fa "$(MAJOR_VER)" -m "release: update $(MAJOR_VER) tag"; \
		git push origin "$(MAJOR_VER)" --force; \
		if [ ! -z "$$GITHUB_OUTPUT" ]; then \
			echo "status=created" >> "$$GITHUB_OUTPUT"; \
		fi; \
	fi

.PHONY: azure-devops
azure-devops:
	$(MAKE) -C .github/azure-devops VERSION=$(VERSION) version vsix

.PHONY: azure-devops-dev
azure-devops-dev:
	$(MAKE) -C .github/azure-devops VERSION=$(VERSION) version-dev vsix

.PHONY: manifest
manifest:
	go run -tags manifest ./cmd/gen github-manifest
	go run -tags manifest ./cmd/gen gitlab-template -o gitlab
	go run -tags manifest ./cmd/gen teamcity-template -o teamcity -v $(VERSION)
	go run -tags manifest ./cmd/gen azure-task -t ./.github/azure-devops/action/task.json
