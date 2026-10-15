#!/usr/bin/env bash
set -euo pipefail
base=$(cd "$(dirname "$0")" && pwd)
# All Kubernetes operations target the user's authorized local cluster.
if [[ $(kubectl config current-context) != orbstack ]]; then
  echo '请先切换到 OrbStack context：kubectl config use-context orbstack' >&2
  exit 1
fi
credentials=${ARTIFACT_STORAGE_CREDENTIALS:-/private/tmp/artifact-storage/credentials.env}
if ! kubectl --context=orbstack -n artifact-storage get secret storage-credentials >/dev/null 2>&1; then
  python3 - "$credentials" <<'PY'
import os, secrets, sys
from pathlib import Path
p = Path(sys.argv[1])
p.parent.mkdir(parents=True, exist_ok=True)
if not p.exists():
    fd = os.open(p, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write('MINIO_ROOT_USER=artifactgateway\nMINIO_ROOT_PASSWORD=' + secrets.token_hex(24) + '\n')
else:
    p.chmod(0o600)
PY
  kubectl --context=orbstack create namespace artifact-storage --dry-run=client -o yaml | kubectl --context=orbstack apply -f -
  kubectl --context=orbstack -n artifact-storage create secret generic storage-credentials --from-env-file="$credentials"
fi
if ! docker image inspect artifact-juicefs:1.4.1-arm64 >/dev/null 2>&1; then
  "$base/build-juicefs.sh"
fi
# JuiceFS stores its data blocks directly in the local SSD-backed PVC.
# On an existing Redis volume, --no-update preserves the original format.
kubectl --context=orbstack apply -f "$base/orbstack.yaml"
kubectl --context=orbstack -n artifact-storage rollout status deployment/storage --timeout=120s
printf '部署完成。Secret: artifact-storage/storage-credentials\n'
printf '运行 %s/port-forward.sh 取得 JuiceFS S3 入口 http://127.0.0.1:19010。\n' "$base"
