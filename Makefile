.PHONY: build build-web build-server dev-server dev-web test run \
	docker-builder docker-build docker-build-local docker-push

build: build-web build-server

build-web:
	cd web && npm ci && npm run build

build-server:
	go build -o bin/server ./cmd/server

# Run these in two terminals during development; Vite proxies /api to :8080.
dev-server:
	go run ./cmd/server

dev-web:
	cd web && npm run dev

test:
	go test ./...

run: build
	./bin/server

# --- Docker -------------------------------------------------------------
#
# Multi-arch images are built with `docker buildx`. Override DOCKER_IMAGE
# with your own repo, e.g.:
#   make docker-push DOCKER_IMAGE=youruser/video-queue DOCKER_TAG=v1.0.0
# `docker login` first; pushing isn't done on your behalf.
#
# Every build/push applies two tags in one invocation: the moving DOCKER_TAG
# (default "latest") and an immutable, timestamped DOCKER_DATE_TAG (UTC).

DOCKER_IMAGE     ?= danotchuuy/video-queue
DOCKER_TAG       ?= latest
DOCKER_DATE_TAG  ?= $(shell date -u +%Y.%m.%d-%H%M)
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64
DOCKER_BUILDER   ?= video-queue-builder

# Ensures a buildx builder that can produce multi-platform images exists and is selected.
docker-builder:
	@docker buildx inspect $(DOCKER_BUILDER) >/dev/null 2>&1 || \
		docker buildx create --name $(DOCKER_BUILDER) --driver docker-container --use
	docker buildx use $(DOCKER_BUILDER)

# Validates that the image builds for every platform (multi-platform results
# can't be loaded locally; use docker-build-local to run one).
docker-build: docker-builder
	docker buildx build \
		--platform $(DOCKER_PLATFORMS) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):$(DOCKER_DATE_TAG) \
		.

# Builds for this machine's platform and loads it into Docker for `docker run`.
docker-build-local: docker-builder
	docker buildx build --load \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):$(DOCKER_DATE_TAG) \
		.

# Builds for every platform and pushes both tags.
docker-push: docker-builder
	docker buildx build \
		--platform $(DOCKER_PLATFORMS) \
		--tag $(DOCKER_IMAGE):$(DOCKER_TAG) \
		--tag $(DOCKER_IMAGE):$(DOCKER_DATE_TAG) \
		--push \
		.
