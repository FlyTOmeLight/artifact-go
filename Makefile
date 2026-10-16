export GOWORK := off
export CGO_ENABLED := 1

.PHONY: build test check fmt doc
build:
	go build -o bin/artifact-server ./cmd/server
	go build -o bin/artifact-forge-server ./cmd/forge-server
	go build -o bin/artifact-maintenance ./cmd/maintenance
test:
	go test ./...
fmt:
	gorgeous ./...
doc:
	bash scripts/generate-openapi.sh
check:
	gorgeous -d ./...
	python3 scripts/check.py
	@set -e; before=$$(shasum docs/openapi.json docs/openapi.yaml); $(MAKE) doc; after=$$(shasum docs/openapi.json docs/openapi.yaml); test "$$before" = "$$after"
