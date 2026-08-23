AGENTS ?= claude,codex
SCENARIO ?= list-apps

.PHONY: build app test smoke stress agent-smoke check-docs check-repo ci release-package npm-build npm-publish

build:
	swift build

app:
	./scripts/build-open-computer-use-app.sh debug

test:
	swift test

smoke:
	./scripts/run-tool-smoke-tests.sh

stress:
	./scripts/run-tool-stress-tests.sh

agent-smoke:
	node ./scripts/run-agent-smoke-tests.mjs --agents=$(AGENTS) --scenario=$(SCENARIO)

check-docs:
	./scripts/check-docs.sh

check-repo:
	./scripts/check-docs.sh
	./scripts/check-repo-hygiene.sh

ci:
	./scripts/ci.sh

release-package:
	./scripts/release-package.sh

npm-build:
	node ./scripts/npm/build-packages.mjs

npm-publish:
	node ./scripts/npm/publish-packages.mjs
