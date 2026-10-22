# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Provision one temporary ordinary IAM issuer and execute T08 live Go tests."""

import argparse
import json
import os
import secrets
import shlex
import subprocess
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--credentials-file", required=True, type=Path)
args = parser.parse_args()
root = {}
for line in args.credentials_file.read_text().splitlines():
    if "=" in line and not line.startswith("#"):
        k, v = line.removeprefix("export ").split("=", 1)
        root[k] = shlex.split(v)[0]
run = "t08-" + secrets.token_hex(6)
password = secrets.token_urlsafe(30)
helper = Path("/private/tmp/artifact-t08-admin")
subprocess.run(
    ["go", "build", "-o", str(helper), "."], cwd="scripts/juicefs-t00-admin", check=True
)


def admin(op, document=None):
    result = subprocess.run(
        [str(helper)],
        input=json.dumps(
            dict(
                Root=root["MINIO_ROOT_USER"],
                Secret=root["MINIO_ROOT_PASSWORD"],
                User=run,
                Password=password,
                Policy=run,
                Operation=op,
                Document=document,
            )
        ),
        text=True,
        capture_output=True,
    )
    if result.returncode:
        raise RuntimeError("IAM operation failed: " + op)


created = policy = private_created = False
try:
    admin("create")
    created = True
    admin(
        "policy",
        {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": [
                        "s3:PutObject",
                        "s3:GetObject",
                        "s3:ListMultipartUploadParts",
                        "s3:AbortMultipartUpload",
                    ],
                    "Resource": ["arn:aws:s3:::tenant-t-*/*"],
                }
            ],
        },
    )
    policy = True
    admin("attach")
    env = os.environ.copy()
    env.update(
        ARTIFACT_TEST_S3_ENDPOINT="http://127.0.0.1:19010",
        ARTIFACT_TEST_S3_ACCESS_KEY_ID=root["MINIO_ROOT_USER"],
        ARTIFACT_TEST_S3_SECRET_ACCESS_KEY=root["MINIO_ROOT_PASSWORD"],
        ARTIFACT_TEST_STS_ACCESS_KEY_ID=run,
        ARTIFACT_TEST_STS_SECRET_ACCESS_KEY=password,
        GOWORK="off",
        GOCACHE="/private/tmp/artifact-go-build",
    )
    # A private environment file lets the separate HTTP/DB acceptance runner use
    # the same temporary issuer while this test waits for actual expiration.
    private = Path("/private/tmp/artifact-t08-live.env")
    private.touch(mode=0o600, exist_ok=False)
    private_created = True
    private.write_text(
        "\n".join(
            k + "=" + shlex.quote(v)
            for k, v in env.items()
            if k.startswith("ARTIFACT_TEST_")
        )
        + "\n"
    )
    env["ARTIFACT_TEST_STS_EXPIRY"] = "1"
    result = subprocess.run(
        [
            "go",
            "test",
            "./tests/integration",
            "-run",
            "^TestStorageGrantAdapterLive$",
            "-count=1",
            "-timeout=20m",
            "-v",
        ],
        env=env,
    )
    if result.returncode:
        raise RuntimeError("live Go acceptance failed")
finally:
    if private_created:
        Path("/private/tmp/artifact-t08-live.env").unlink(missing_ok=True)
    if created:
        admin("delete-user")
    if policy:
        admin("delete-policy")
    helper.unlink(missing_ok=True)
