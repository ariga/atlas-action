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
	aws s3 cp ./atlas-action s3://release.ariga.io/atlas-action/$(BINARY_NAME)-$(MAJOR_VER);

.PHONY: docker-build
docker-build:
	docker build \
		--label org.opencontainers.image.revision=$(COMMIT) \
		--label org.opencontainers.image.created=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):$(MAJOR_VER) .

.PHONY: docker-push
docker-push:
	docker push $(DOCKER_IMAGE):$(VERSION)
	docker push $(DOCKER_IMAGE):$(MAJOR_VER)

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
	go run -tags manifest ./cmd/gen azure-task -t ./.github/azure-devops/action/task.json
