include .makerc

GIT_CHGLOG_VERSION        ?= v0.15.0

RELEASER_IMAGE            := ghcr.io/goreleaser/goreleaser-cross-base:v$(GOLANG_VERSION)

GORELEASER_SKIP_VALIDATE  ?= false
GORELEASER_SNAPSHOT       ?= false

GO_MOD_NAME               ?= $(shell go list)
GORELEASER_RUN            := docker run --privileged --rm -v /var/run/docker.sock:/var/run/docker.sock -v `pwd`:/go/src/$(GO_MOD_NAME) -w /go/src/$(GO_MOD_NAME)
GORELEASER_RELEASE_NOTES  := --release-notes=/go/src/$(GO_MOD_NAME)/.cache/changelog.md
GORELEASER_RELEASE_FOOTER ?= --release-footer=/go/src/$(GO_MOD_NAME)/.github/release-footer.gotmpl
CHANGELOG                 := .cache/changelog.md
GORELEASER_TAG            ?= $(shell git describe --tags --abbrev=0)

CACHE                     ?= .cache
DEVCACHE                  := $(abspath $(CACHE))
DEVCACHE_BIN              := $(DEVCACHE)/bin
DEVCACHE_VERSIONS         := $(DEVCACHE)/versions

INVENTORY                 := $(DEVCACHE)/bin/inventory
GIT_CHGLOG                := $(DEVCACHE_BIN)/git-chglog

GIT_CHGLOG_VERSION_FILE   := $(DEVCACHE_VERSIONS)/git-chglog/$(GIT_CHGLOG_VERSION)

$(DEVCACHE):
	mkdir -p $(DEVCACHE)
	mkdir -p $(DEVCACHE_BIN)
	mkdir -p $(DEVCACHE_VERSIONS)

$(GIT_CHGLOG_VERSION_FILE): $(DEVCACHE)
	@echo "installing git-chglog $(GIT_CHGLOG_VERSION) ..."
	rm -f $(GIT_CHGLOG)
	GOBIN=$(DEVCACHE_BIN) go install github.com/git-chglog/git-chglog/cmd/git-chglog@$(GIT_CHGLOG_VERSION)
	rm -rf "$(dir $@)"
	mkdir -p "$(dir $@)"
	touch $@
$(GIT_CHGLOG): $(GIT_CHGLOG_VERSION_FILE)

$(CHANGELOG): $(GIT_CHGLOG)
	@echo "generating changelog to .cache/changelog"
	./script/genchangelog.sh "$(GORELEASER_TAG)" .cache/changelog.md

.PHONY: changelog
changelog: $(CHANGELOG)

.PHONY: release-dry-run
release-dry-run: $(CHANGELOG)
	$(GORELEASER_RUN) $(RELEASER_IMAGE) -f .goreleaser.yaml \
		--rm-dist \
		--skip-publish \
		--skip-validate=$(GORELEASER_SKIP_VALIDATE) \
		--snapshot=$(GORELEASER_SNAPSHOT) \
		$(GORELEASER_RELEASE_NOTES)

.PHONY: release
release: $(CHANGELOG)
	$(GORELEASER_RUN) \
		--env-file .release-env \
		$(RELEASER_IMAGE) \
		-f .goreleaser.yaml release $(GORELEASER_RELEASE_NOTES) --rm-dist

.PHONY: bins
bins: $(INVENTORY)

.PHONY: $(INVENTORY)
$(INVENTORY):
	GOOS=linux GOARCH=amd64 go build -o inventory

.PHONY: docker
docker: bins
	docker build --platform=linux/amd64 . -t ghcr.io/ovrclk/k8s-inventory-operator
