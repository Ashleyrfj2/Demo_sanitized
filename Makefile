.PHONY: test validate test-integration run-api

test:
	node scripts/check-public-secrets.mjs
	node scripts/check-contract-conformance.mjs
	go test ./backend/internal/...
	npm test --prefix frontend
	npm test --prefix extension

validate: test

test-integration:
	./scripts/test-integration.sh

run-api:
	go run ./backend/cmd/api
