#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# swag emits Swagger/OpenAPI 2.0 from controller annotations and the SDK DTOs.
if [[ "$(swag --version)" != "swag version v1.8.4" ]]; then
  echo 'Install the pinned generator: go install github.com/swaggo/swag/cmd/swag@v1.8.4' >&2
  exit 1
fi
framework_dir=$(GOWORK=off go list -m -f '{{.Dir}}' github.com/FlyTOmeLight/server-go)
temporary_output=$(mktemp -d)
trap 'rm -rf "$temporary_output"' EXIT
swag init -d "internal/app/artifact,internal/app/version,internal/app/alias,internal/app/retention,internal/app/draft,internal/app/resolve,internal/app/storage,sdk/go/artifactv1,sdk/go/storagev1,$framework_dir/errors" \
  -g controller.go --parseInternal --outputTypes json,yaml -o "$temporary_output"
cp "$temporary_output/swagger.json" docs/openapi.json
cp "$temporary_output/swagger.yaml" docs/openapi.yaml
