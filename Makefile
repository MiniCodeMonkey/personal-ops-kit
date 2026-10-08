# Personal ops build. ./install.sh is the usual way in; this is what it calls.
#
# Everything the daemon serves is compiled in, so `make` produces binaries that
# depend on nothing else at runtime -- no interpreter to find on launchd's near-empty
# PATH, no stylesheet to locate on disk.

BIN        := bin
TAILWIND   := $(BIN)/tailwindcss
CSS_IN     := internal/ui/assets/input.css
CSS_OUT    := internal/ui/assets/app.css
GOFILES    := $(shell find . -name '*.go' -not -path './.git/*' 2>/dev/null)

# Pinned rather than "latest": a silent minor bump to the CSS toolchain is not
# something a scheduled job should discover on its own.
TAILWIND_VERSION := v4.3.3
UNAME_M          := $(shell uname -m)
ifeq ($(UNAME_M),arm64)
  TAILWIND_ARCH := macos-arm64
else
  TAILWIND_ARCH := macos-x64
endif
TAILWIND_URL := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_ARCH)

.PHONY: all build css test fmt vet clean reload install-daemon uninstall-daemon tools

all: build

# The compiled stylesheet is committed, so a plain build needs only Go. Run
# `make css` after changing the dashboard template's classes.
build:
	@mkdir -p $(BIN)
	go build -o $(BIN)/jobctl          ./cmd/jobctl
	go build -o $(BIN)/lanepick        ./cmd/lanepick
	go build -o $(BIN)/transcriptminer ./cmd/transcriptminer
	go build -o $(BIN)/weeklyreview    ./cmd/weeklyreview
	go build -o $(BIN)/platformaudit   ./cmd/platformaudit
	@echo "built -> $(BIN)/"

# The standalone Tailwind binary keeps Node out of the build entirely. It is
# downloaded on demand and gitignored; only the compiled CSS is committed, so a
# clone can build without network access to a CSS toolchain.
$(TAILWIND):
	@mkdir -p $(BIN)
	curl -sSfL -o $@ $(TAILWIND_URL)
	chmod +x $@

tools: $(TAILWIND)

css: $(TAILWIND)
	$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT) --minify

test:
	go test ./...
	bash lib/gate_test.sh
	bash test/install_test.sh

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN)

# The usual edit loop: rebuild and restart in one step.
reload: build
	./$(BIN)/jobctl reload

install-daemon: build
	./$(BIN)/jobctl install-daemon

uninstall-daemon:
	./$(BIN)/jobctl uninstall-daemon
