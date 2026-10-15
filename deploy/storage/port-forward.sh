#!/usr/bin/env bash
set -euo pipefail
exec kubectl --context=orbstack -n artifact-storage port-forward --address=127.0.0.1 service/juicefs 19010:9000
