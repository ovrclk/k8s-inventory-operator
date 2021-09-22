GORELEASER_VERSION       ?= v0.179.0
GORELEASER_IMAGE         := ghcr.io/goreleaser/goreleaser:$(GORELEASER_VERSION)
GORELEASER_SKIP_VALIDATE ?= false
GORELEASER_SNAPSHOT      ?= false
GO_MOD_NAME              ?= $(shell go list)
GORELEASER_RUN           := docker run --rm -v `pwd`:/go/src/$(GO_MOD_NAME) -w /go/src/$(GO_MOD_NAME) $(GORELEASER_IMAGE)

.PHONY: release-dry-run
release-dry-run:
	$(GORELEASER_RUN) -f .goreleaser.yaml --rm-dist --skip-publish --skip-validate=$(GORELEASER_SKIP_VALIDATE) --snapshot=$(GORELEASER_SNAPSHOT)

.PHONY: release
release:
