LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	[ -d $@ ] || mkdir -p $@

## Tool binary names.
TYPOS = $(LOCALBIN)/typos

## Tool versions.
TYPOS_VERSION ?= v1.34.0

.PHONY: typos
typos: $(TYPOS)
$(TYPOS): | $(LOCALBIN)
	@echo "Downloading typos $(TYPOS_VERSION)..."
	curl -L https://github.com/crate-ci/typos/releases/download/$(TYPOS_VERSION)/typos-$(TYPOS_VERSION)-x86_64-unknown-linux-musl.tar.gz | tar -xz -C $(LOCALBIN) --wildcards '*/typos'
	chmod +x $(TYPOS)
