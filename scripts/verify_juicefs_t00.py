# /// script
# requires-python = ">=3.11"
# dependencies = ["boto3==1.43.113"]
# ///
"""T00 live JuiceFS probe. Credentials stay in memory; only own resources removed."""

import argparse
import hashlib
import json
import secrets
import shlex
import subprocess
import time
import xml.etree.ElementTree as ET
from datetime import UTC, datetime
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

import boto3
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.config import Config
from botocore.credentials import Credentials
from botocore.exceptions import BotoCoreError, ClientError

ENDPOINT = "http://127.0.0.1:19010"
REPORT = (
    Path(__file__).resolve().parents[1]
    / "docs/storage-contract/t00-juicefs-report.json"
)
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--admin-helper", required=True, type=Path)
args = parser.parse_args()
checks = []
run = "t00-" + secrets.token_hex(6)
env = {}
for line in (
    Path("/private/tmp/artifact-storage/credentials.env").read_text().splitlines()
):
    if "=" in line and not line.startswith("#"):
        key, value = line.removeprefix("export ").split("=", 1)
        env[key] = shlex.split(value)[0]
root, secret = env["MINIO_ROOT_USER"], env["MINIO_ROOT_PASSWORD"]
user, password = run, secrets.token_urlsafe(30)
config = Config(
    signature_version="s3v4",
    s3={"addressing_style": "path"},
    retries={"total_max_attempts": 1},
    connect_timeout=5,
    read_timeout=15,
)


def client(access=root, private=secret, token=None):
    return boto3.client(
        "s3",
        endpoint_url=ENDPOINT,
        region_name="us-east-1",
        aws_access_key_id=access,
        aws_secret_access_key=private,
        aws_session_token=token,
        config=config,
    )


def record(name, status, **details):
    checks.append(dict(name=name, status=status, **details))
    REPORT.write_text(
        json.dumps(
            {
                "run": run,
                "backend": "JuiceFS 1.4.1",
                "endpoint": ENDPOINT,
                "updated_at": datetime.now(UTC).isoformat(),
                "checks": checks,
            },
            indent=2,
        )
        + "\n"
    )
    print(name, status, flush=True)


def attempt(name, call, denied=False):
    try:
        result = call()
        record(name, "fail" if denied else "pass")
        return result
    except ClientError as error:
        code = error.response["Error"]["Code"]
        record(
            name,
            "pass"
            if denied and code in {"AccessDenied", "ExpiredToken", "InvalidToken"}
            else "fail",
            error_code=code,
        )
    except (BotoCoreError, URLError, OSError, ET.ParseError) as error:
        record(name, "fail", error_type=type(error).__name__)


def admin(operation, document=None):
    data = {
        "Root": root,
        "Secret": secret,
        "User": user,
        "Password": password,
        "Policy": run,
        "Operation": operation,
        "Document": document,
    }
    result = subprocess.run(
        [str(args.admin_helper)],
        check=False,
        input=json.dumps(data),
        text=True,
        capture_output=True,
        timeout=40,
    )
    if result.returncode:
        # SDK error can contain context; do not print full arbitrary output.
        raise RuntimeError("IAM admin operation failed: " + operation)


def grant(actions, keys, duration=900):
    policy = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Action": actions,
                "Resource": [f"arn:aws:s3:::artifact-data/{k}" for k in keys],
            }
        ],
    }
    body = urlencode(
        {
            "Action": "AssumeRole",
            "Version": "2011-06-15",
            "DurationSeconds": duration,
            "Policy": json.dumps(policy),
        }
    )
    request = AWSRequest(
        method="POST",
        url=ENDPOINT,
        data=body,
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "x-amz-content-sha256": hashlib.sha256(body.encode()).hexdigest(),
        },
    )
    SigV4Auth(Credentials(user, password), "sts", "us-east-1").add_auth(request)
    with urlopen(
        Request(ENDPOINT, data=body.encode(), headers=dict(request.headers)), timeout=20
    ) as response:
        fields = {
            element.tag.rsplit("}", 1)[-1]: element.text
            for element in ET.fromstring(response.read()).iter()
        }
    return client(
        fields["AccessKeyId"], fields["SecretAccessKey"], fields["SessionToken"]
    ), datetime.fromisoformat(fields["Expiration"])


def main():
    service = client()
    keys = [
        run + "/one",
        run + "/two",
        run + "/multipart",
        run + "/orphan",
        run + "/outside",
    ]
    uploads = []
    created = False
    policy_created = False
    try:
        attempt("root_list_buckets", service.list_buckets)
        try:
            admin("create")
            created = True
            record("temporary_iam_create", "pass")
            document = {
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
                        "Resource": [
                            f"arn:aws:s3:::{bucket}/{run}/*"
                            for bucket in ["artifact-data", "artifact-models"]
                        ],
                    }
                ],
            }
            admin("policy", document)
            policy_created = True
            admin("attach")
            record("temporary_iam_policy", "pass")
            for duration in [30, 60, 899]:
                try:
                    _, expires = grant(["s3:PutObject"], keys[:1], duration)
                    record(
                        f"duration_{duration}",
                        "pass",
                        accepted=True,
                        expires_at=expires.isoformat(),
                    )
                except HTTPError as error:
                    fields = {
                        element.tag.rsplit("}", 1)[-1]: element.text
                        for element in ET.fromstring(error.read()).iter()
                    }
                    record(
                        f"duration_{duration}",
                        "pass",
                        accepted=False,
                        http_status=error.code,
                        error_code=fields.get("Code"),
                        message=fields.get("Message"),
                    )
            try:
                grant(["s3:PutObject"], [run + "/" + str(i) for i in range(1000)])
                record("policy_1000_keys", "pass", accepted=True)
            except HTTPError as error:
                fields = {
                    element.tag.rsplit("}", 1)[-1]: element.text
                    for element in ET.fromstring(error.read()).iter()
                }
                record(
                    "policy_1000_keys",
                    "pass",
                    accepted=False,
                    http_status=error.code,
                    error_code=fields.get("Code"),
                    message=fields.get("Message"),
                )
        except RuntimeError:
            record(
                "ordinary_iam_setup",
                "fail",
                reason="admin API rejected operation; no root STS substitution",
            )
        if created and policy_created:
            try:
                upload, expiration = grant(
                    [
                        "s3:PutObject",
                        "s3:ListMultipartUploadParts",
                        "s3:AbortMultipartUpload",
                    ],
                    keys[:4],
                )
                record(
                    "ordinary_iam_upload_sts", "pass", expires_at=expiration.isoformat()
                )
                for key in keys[:2]:
                    attempt(
                        "multi_key_put_" + key.rsplit("/", 1)[1],
                        lambda key=key: upload.put_object(
                            Bucket="artifact-data", Key=key, Body=b"0123456789"
                        ),
                    )
                attempt(
                    "outside_key_denied",
                    lambda: upload.put_object(
                        Bucket="artifact-data", Key=keys[4], Body=b"x"
                    ),
                    True,
                )
                attempt(
                    "other_bucket_denied",
                    lambda: upload.put_object(
                        Bucket="artifact-models", Key=keys[0], Body=b"x"
                    ),
                    True,
                )
                attempt(
                    "delete_denied",
                    lambda: upload.delete_object(Bucket="artifact-data", Key=keys[0]),
                    True,
                )
                attempt(
                    "upload_get_denied",
                    lambda: upload.get_object(Bucket="artifact-data", Key=keys[0]),
                    True,
                )
                attempt(
                    "list_bucket_denied",
                    lambda: upload.list_objects_v2(
                        Bucket="artifact-data", Prefix=run + "/"
                    ),
                    True,
                )
                for key in keys[2:4]:
                    begun = upload.create_multipart_upload(
                        Bucket="artifact-data", Key=key
                    )
                    args = {
                        "Bucket": "artifact-data",
                        "Key": key,
                        "UploadId": begun["UploadId"],
                    }
                    uploads.append(args)
                    part = upload.upload_part(
                        **args, PartNumber=1, Body=b"m" * (5 * 1024 * 1024)
                    )
                    attempt(
                        "multipart_list_" + key.rsplit("/", 1)[1],
                        lambda args=args: upload.list_parts(**args),
                    )
                    if key == keys[2]:
                        attempt(
                            "multipart_complete",
                            lambda args=args, part=part: (
                                upload.complete_multipart_upload(
                                    **args,
                                    MultipartUpload={
                                        "Parts": [
                                            {"PartNumber": 1, "ETag": part["ETag"]}
                                        ]
                                    },
                                )
                            ),
                        )
                    else:
                        listed = service.list_multipart_uploads(
                            Bucket="artifact-data", Prefix=run + "/"
                        ).get("Uploads", [])
                        record(
                            "orphan_visible_before_abort",
                            "pass"
                            if any(
                                item["UploadId"] == args["UploadId"] for item in listed
                            )
                            else "fail",
                        )
                        attempt(
                            "multipart_abort",
                            lambda args=args: upload.abort_multipart_upload(**args),
                        )
                        listed = service.list_multipart_uploads(
                            Bucket="artifact-data", Prefix=run + "/"
                        ).get("Uploads", [])
                        record(
                            "orphan_absent_after_abort",
                            "pass"
                            if not any(
                                item["UploadId"] == args["UploadId"] for item in listed
                            )
                            else "fail",
                        )
                download, _ = grant(["s3:GetObject"], keys[:2])
                for name, kwargs in [("get", {}), ("range", {"Range": "bytes=2-4"})]:
                    response = download.get_object(
                        Bucket="artifact-data", Key=keys[0], **kwargs
                    )
                    with response["Body"] as body:
                        data = body.read()
                    record(
                        "download_" + name,
                        "pass"
                        if data == (b"234" if kwargs else b"0123456789")
                        else "fail",
                    )
                attempt(
                    "download_head",
                    lambda: download.head_object(Bucket="artifact-data", Key=keys[0]),
                )
                service.put_object(Bucket="artifact-data", Key=keys[4], Body=b"outside")
                service.put_object(
                    Bucket="artifact-models", Key=keys[0], Body=b"outside"
                )
                attempt(
                    "download_other_key_denied",
                    lambda: download.get_object(Bucket="artifact-data", Key=keys[4]),
                    True,
                )
                attempt(
                    "download_other_bucket_denied",
                    lambda: download.get_object(Bucket="artifact-models", Key=keys[0]),
                    True,
                )
                attempt(
                    "download_put_denied",
                    lambda: download.put_object(
                        Bucket="artifact-data", Key=keys[0], Body=b"bad"
                    ),
                    True,
                )
                custom, _ = grant(["s3:PutObject"], keys[:1])
                attempt(
                    "custom_put",
                    lambda: custom.put_object(
                        Bucket="artifact-data", Key=keys[0], Body=b"0123456789"
                    ),
                )
                attempt(
                    "custom_abort_denied",
                    lambda: custom.abort_multipart_upload(**uploads[-1]),
                    True,
                )
                record(
                    "expiration_wait_started",
                    "info",
                    expires_at=expiration.isoformat(),
                )
                while datetime.now(UTC).timestamp() <= expiration.timestamp() - 10:
                    time.sleep(
                        min(20, max(0.1, expiration.timestamp() - 9 - time.time()))
                    )
                attempt(
                    "sts_put_before_expiration",
                    lambda: upload.put_object(
                        Bucket="artifact-data", Key=keys[0], Body=b"before"
                    ),
                )
                while datetime.now(UTC).timestamp() <= expiration.timestamp() + 2:
                    time.sleep(
                        min(20, max(0.1, expiration.timestamp() + 3 - time.time()))
                    )
                try:
                    upload.put_object(Bucket="artifact-data", Key=keys[0], Body=b"late")
                    record(
                        "expired_sts_put_denied",
                        "fail",
                        reason="expired credential accepted",
                    )
                except ClientError as error:
                    code = error.response["Error"]["Code"]
                    record(
                        "expired_sts_put_denied",
                        "pass" if code == "ExpiredToken" else "pending",
                        error_code=code,
                        reason="Only ExpiredToken unambiguously proves expiration; other denial needs backend evidence",
                    )
            except HTTPError as error:
                record("ordinary_iam_sts", "fail", http_status=error.code)
            except (BotoCoreError, URLError, OSError, ET.ParseError) as error:
                record("sts_sequence", "fail", error_type=type(error).__name__)
        # Service data path remains useful even if IAM/STS is unavailable.
        attempt(
            "service_put",
            lambda: service.put_object(
                Bucket="artifact-data", Key=keys[0], Body=b"0123456789"
            ),
        )

        def add_origin(params, **kwargs):
            params["headers"]["Origin"] = "http://localhost:3000"

        service.meta.events.register("before-call.s3.GetObject", add_origin)
        response = service.get_object(Bucket="artifact-data", Key=keys[0])
        response["Body"].close()
        headers = response["ResponseMetadata"]["HTTPHeaders"]
        record(
            "cors_actual_get",
            "pass"
            if headers.get("access-control-allow-origin") == "http://localhost:3000"
            else "fail",
            headers={
                key: value
                for key, value in headers.items()
                if key.startswith("access-control")
            },
        )
        try:
            req = Request(
                ENDPOINT + "/artifact-data/" + keys[0],
                method="OPTIONS",
                headers={
                    "Origin": "http://localhost:3000",
                    "Access-Control-Request-Method": "PUT",
                    "Access-Control-Request-Headers": "authorization,x-amz-date,x-amz-security-token,x-amz-content-sha256,content-type",
                },
            )
            with urlopen(req, timeout=10) as response:
                headers = dict(response.headers)
            record(
                "cors_preflight",
                "pass"
                if any(k.lower() == "access-control-allow-origin" for k in headers)
                else "fail",
                headers={
                    k: v
                    for k, v in headers.items()
                    if k.lower().startswith("access-control")
                },
            )
        except HTTPError as error:
            record("cors_preflight", "fail", http_status=error.code)
    finally:
        for args in uploads:
            try:
                service.abort_multipart_upload(**args)
            except ClientError as error:
                if error.response["Error"]["Code"] != "NoSuchUpload":
                    record(
                        "cleanup_multipart",
                        "fail",
                        error_code=error.response["Error"]["Code"],
                    )
        for bucket in ["artifact-data", "artifact-models"]:
            for key in keys:
                attempt(
                    "cleanup_delete",
                    lambda bucket=bucket, key=key: service.delete_object(
                        Bucket=bucket, Key=key
                    ),
                )
        try:
            service.head_object(Bucket="artifact-data", Key=keys[0])
            record("delete_head_absent", "fail")
        except ClientError as error:
            record(
                "delete_head_absent",
                "pass" if error.response["Error"]["Code"] == "404" else "fail",
                note="S3 absence only; trash-days=1 prevents immediate physical-space claim",
            )
        if created:
            try:
                admin("delete-user")
                record("temporary_iam_deleted", "pass")
            except RuntimeError:
                record("temporary_iam_deleted", "fail")
        if policy_created:
            try:
                admin("delete-policy")
                record("temporary_policy_deleted", "pass")
            except RuntimeError:
                record("temporary_policy_deleted", "fail")


if __name__ == "__main__":
    main()
    if any(check["status"] == "fail" for check in checks):
        raise SystemExit(1)
    if any(check["status"] == "pending" for check in checks):
        raise SystemExit(2)
