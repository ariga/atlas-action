# Copyright 2021-present The Atlas Authors. All rights reserved.
# This source code is licensed under the Apache 2.0 license found
# in the LICENSE file in the root directory of this source tree.

BUILD_DATE  = $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION     = $(shell cat VERSION.txt)
COMMIT     ?= $(shell git rev-parse --short HEAD)
FULL_COMMIT = $(shell git rev-parse HEAD)
MAJOR_VER   = $(shell echo "$(VERSION)" | cut -d. -f1)
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

# Publish the release binaries and scripts to the CDN: S3 (release.ariga.io)
# and R2 (a mirror).
#
# Keys named after a full version or a commit are immutable, the bucket policy
# only accepts them with "If-None-Match: *". They are written by s3-put-once.sh
# first, which treats an existing key with identical content as already
# published (a re-run) and fails on different content. Only then are the
# rolling "-$(MAJOR_VER)" keys, which change on every release, overwritten.
# For every key S3 comes before R2, so a conflict aborts before R2 is touched.
#
# Every command is its own recipe line: make stops at the first one that fails.
S3_BUCKET   ?= release.ariga.io
R2_BUCKET   ?= atlas-binaries
S3_PUT_ONCE  = bash .github/scripts/s3-put-once.sh $(S3_BUCKET)
S3_DEST      = s3://$(S3_BUCKET)/atlas-action
R2_DEST      = r2:$(R2_BUCKET)/atlas-action

.PHONY: s3-upload
s3-upload: $(ATLAS_BIN_LINUX_AMD64) $(ATLAS_BIN_LINUX_ARM64)
	@echo "==> S3: write-once keys"
	$(S3_PUT_ONCE) atlas-action/scripts-$(VERSION)/shim.sh ./scripts/shim.sh application/x-sh
	$(S3_PUT_ONCE) atlas-action/scripts-$(VERSION)/setup-atlas.sh ./scripts/setup-atlas.sh application/x-sh
	$(S3_PUT_ONCE) atlas-action/$(BINARY_NAME)-$(VERSION) ./$(ATLAS_BIN_LINUX_AMD64) binary/octet-stream
	$(S3_PUT_ONCE) atlas-action/$(BINARY_NAME)-$(FULL_COMMIT) ./$(ATLAS_BIN_LINUX_AMD64) binary/octet-stream
	$(S3_PUT_ONCE) atlas-action/$(ATLAS_BIN_LINUX_AMD64)-$(VERSION) ./$(ATLAS_BIN_LINUX_AMD64) binary/octet-stream
	$(S3_PUT_ONCE) atlas-action/$(ATLAS_BIN_LINUX_AMD64)-$(FULL_COMMIT) ./$(ATLAS_BIN_LINUX_AMD64) binary/octet-stream
	$(S3_PUT_ONCE) atlas-action/$(ATLAS_BIN_LINUX_ARM64)-$(VERSION) ./$(ATLAS_BIN_LINUX_ARM64) binary/octet-stream
	$(S3_PUT_ONCE) atlas-action/$(ATLAS_BIN_LINUX_ARM64)-$(FULL_COMMIT) ./$(ATLAS_BIN_LINUX_ARM64) binary/octet-stream
	@echo "==> R2: write-once keys"
	rclone copyto ./scripts/shim.sh $(R2_DEST)/scripts-$(VERSION)/shim.sh
	rclone copyto ./scripts/setup-atlas.sh $(R2_DEST)/scripts-$(VERSION)/setup-atlas.sh
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(BINARY_NAME)-$(VERSION)
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(BINARY_NAME)-$(FULL_COMMIT)
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(ATLAS_BIN_LINUX_AMD64)-$(VERSION)
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(ATLAS_BIN_LINUX_AMD64)-$(FULL_COMMIT)
	rclone copyto ./$(ATLAS_BIN_LINUX_ARM64) $(R2_DEST)/$(ATLAS_BIN_LINUX_ARM64)-$(VERSION)
	rclone copyto ./$(ATLAS_BIN_LINUX_ARM64) $(R2_DEST)/$(ATLAS_BIN_LINUX_ARM64)-$(FULL_COMMIT)
	@echo "==> S3 and R2: rolling $(MAJOR_VER) keys"
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) $(S3_DEST)/$(BINARY_NAME)-$(MAJOR_VER)
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(BINARY_NAME)-$(MAJOR_VER)
	aws s3 cp ./$(ATLAS_BIN_LINUX_AMD64) $(S3_DEST)/$(ATLAS_BIN_LINUX_AMD64)-$(MAJOR_VER)
	rclone copyto ./$(ATLAS_BIN_LINUX_AMD64) $(R2_DEST)/$(ATLAS_BIN_LINUX_AMD64)-$(MAJOR_VER)
	aws s3 cp ./$(ATLAS_BIN_LINUX_ARM64) $(S3_DEST)/$(ATLAS_BIN_LINUX_ARM64)-$(MAJOR_VER)
	rclone copyto ./$(ATLAS_BIN_LINUX_ARM64) $(R2_DEST)/$(ATLAS_BIN_LINUX_ARM64)-$(MAJOR_VER)

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
			echo "status=exists" >> "$$GITHUB_OUTPUT"; \
		fi; \
	else \
		echo "Creating new release for version $(VERSION)"; \
		git fetch --force --tags origin; \
		if git rev-parse "$(VERSION)" >/dev/null 2>&1; then \
			tag_commit=$$(git rev-list -n 1 "$(VERSION)"); \
			if [ "$$tag_commit" != "$(FULL_COMMIT)" ]; then \
				echo "Tag $(VERSION) already exists at $$tag_commit, expected $(FULL_COMMIT)." >&2; \
				exit 1; \
			fi; \
		else \
			git tag -a "$(VERSION)" "$(FULL_COMMIT)" -m "release: $(VERSION)"; \
			git push origin "refs/tags/$(VERSION)"; \
		fi; \
		if [ "$$LATEST_VERSION" = "none" ]; then \
			gh release create "$(VERSION)" --verify-tag --generate-notes; \
		else \
			gh release create "$(VERSION)" \
				--verify-tag \
				--notes-start-tag="$$LATEST_VERSION" --generate-notes; \
		fi; \
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
manifest: gitlab-manifest orb-manifest
	go run -tags manifest ./cmd/gen github-manifest
	go run -tags manifest ./cmd/gen teamcity-template -o teamcity -v $(VERSION)
	go run -tags manifest ./cmd/gen azure-task -t ./.github/azure-devops/action/task.json

.PHONY: gitlab-manifest
gitlab-manifest: $(ATLAS_BIN_LINUX_AMD64) $(ATLAS_BIN_LINUX_ARM64)
	go run -tags manifest ./cmd/gen gitlab-template -o gitlab -v $(VERSION) \
		--sha256 linux-amd64=$$(sha256sum $(ATLAS_BIN_LINUX_AMD64) | cut -d' ' -f1) \
		--sha256 linux-arm64=$$(sha256sum $(ATLAS_BIN_LINUX_ARM64) | cut -d' ' -f1)

.PHONY: orb-manifest
orb-manifest:
	go run -tags manifest ./cmd/gen orb-template -o orb -v $(VERSION)
