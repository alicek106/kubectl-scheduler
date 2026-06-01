# kubectl-scheduler — build & package (Apple Silicon macOS / darwin-arm64 only)

VERSION  ?= v0.1.1
BIN      := kubectl-schedule
DIST     := dist
ARCHIVE  := $(DIST)/$(BIN)_$(VERSION)_darwin_arm64.tar.gz
MANIFEST := plugins/schedule.yaml

.PHONY: build dist sha manifest test clean

## build: compile the darwin/arm64 plugin binary into dist/ (reproducible bytes)
build:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
		go build -trimpath -ldflags "-s -w" -o $(DIST)/$(BIN) ./cmd/kubectl-schedule

## dist: build + create a deterministic krew release archive (tar.gz)
## Fixed mtime + numeric owner + gzip -n => stable sha256 across rebuilds.
dist: build
	touch -t 197001010000.00 $(DIST)/$(BIN)
	tar --numeric-owner --uid 0 --gid 0 -cf - -C $(DIST) $(BIN) | gzip -n > $(ARCHIVE)
	@echo "archive: $(ARCHIVE)"

## sha: print the archive sha256
sha: dist
	@shasum -a 256 $(ARCHIVE)

## manifest: build the archive and write its sha256 into plugins/schedule.yaml
## (perl -i is portable across macOS BSD sed / GNU sed environments)
manifest: dist
	$(eval SHA := $(shell shasum -a 256 $(ARCHIVE) | awk '{print $$1}'))
	perl -i -pe 's/^(      sha256: ).*/$${1}$(SHA)/' $(MANIFEST)
	@echo "updated $(MANIFEST) sha256 -> $(SHA)"

## test: run the unit + PoC tests
test:
	go test ./...

clean:
	rm -rf $(DIST)
