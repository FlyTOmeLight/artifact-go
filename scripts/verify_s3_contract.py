# /// script
# requires-python = ">=3.11"
# dependencies = ["boto3==1.43.113"]
# ///
"""Exercise direct uploads and tenant isolation on preconfigured S3 test buckets."""

import argparse
import base64
import hashlib
import json
import sys
import time
import uuid
import xml.etree.ElementTree as ET
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlencode, urlparse
from urllib.request import Request, urlopen

import boto3
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.config import Config
from botocore.exceptions import ClientError


def digest(data):
    return base64.b64encode(hashlib.sha256(data).digest()).decode()


class Probe:
    def __init__(self, args):
        self.args = args
        self.run_id = str(uuid.uuid4())
        self.prefix = f"{args.prefix.rstrip('/')}/{self.run_id}/"
        self.keys = set()
        self.uploads = []
        self.checks = []
        self.session = boto3.Session(profile_name=args.profile, region_name=args.region)
        self.config = Config(
            retries={"total_max_attempts": 1},
            connect_timeout=10,
            read_timeout=30,
            s3={"addressing_style": args.addressing_style},
        )
        self.service = self.session.client(
            "s3", endpoint_url=args.endpoint, config=self.config
        )
        self.grant_session = (
            boto3.Session(profile_name=args.grant_profile, region_name=args.region)
            if args.grant_profile
            else self.session
        )
        self.sts = self.grant_session.client(
            "sts", endpoint_url=args.sts_endpoint, config=self.config
        )

    def record(self, name, status, **details):
        self.checks.append({"name": name, "status": status, **details})
        print(f"{name}: {status}", flush=True)

    def check(self, name, condition, **details):
        self.record(name, "pass" if condition else "fail", **details)
        if not condition:
            raise AssertionError(name)

    def reject(self, name, call, codes):
        if self.args.service_only and name in {
            "other_key_denied",
            "object_delete_denied",
            "bucket_policy_read_denied",
        }:
            self.record(
                name,
                "pending",
                reason="service-only mode cannot prove temporary credential isolation",
            )
            return
        try:
            call()
        except ClientError as error:
            code = error.response["Error"]["Code"]
            self.record(name, "pass" if code in codes else "fail", error_code=code)
        else:
            self.record(name, "fail", reason="unexpected_success")

    def key(self, suffix):
        key = self.prefix + suffix
        self.keys.add((self.args.bucket, key))
        return key

    def grant(self, key):
        if self.args.service_only:
            self.record(
                "grant_deadline", "pending", reason="service-only mode; STS not used"
            )
            return self.service, None
        requested_deadline = datetime.now(UTC) + timedelta(
            seconds=self.args.session_seconds
        )
        policy = {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": [
                        "s3:PutObject",
                        "s3:ListMultipartUploadParts",
                        "s3:AbortMultipartUpload",
                    ],
                    "Resource": [f"arn:aws:s3:::{self.args.bucket}/{key}"],
                }
            ],
        }
        if self.args.backend in {"rustfs", "juicefs"}:
            # RustFS's documented STS endpoint signs with service s3, not AWS STS.
            endpoint = self.args.sts_endpoint or self.args.endpoint
            form = urlencode(
                {
                    "Action": "AssumeRole",
                    "Version": "2011-06-15",
                    "DurationSeconds": self.args.duration,
                    "Policy": json.dumps(policy),
                }
            )
            request = AWSRequest(
                method="POST",
                url=endpoint,
                data=form,
                headers={
                    "Content-Type": "application/x-www-form-urlencoded",
                    "x-amz-content-sha256": hashlib.sha256(form.encode()).hexdigest(),
                },
            )
            SigV4Auth(
                self.grant_session.get_credentials().get_frozen_credentials(),
                "s3" if self.args.backend == "rustfs" else "sts",
                self.args.region,
            ).add_auth(request)
            with urlopen(
                Request(endpoint, data=form.encode(), headers=dict(request.headers)),
                timeout=30,
            ) as response:
                root = ET.fromstring(response.read())
            fields = {
                element.tag.rsplit("}", 1)[-1]: element.text for element in root.iter()
            }
            credentials = {
                key: fields[key]
                for key in ("AccessKeyId", "SecretAccessKey", "SessionToken")
            }
            expiration = datetime.fromisoformat(fields["Expiration"])
        else:
            result = self.sts.assume_role(
                RoleArn=self.args.role_arn,
                RoleSessionName=f"artifact-probe-{self.run_id[:8]}",
                DurationSeconds=self.args.duration,
                Policy=json.dumps(policy),
            )
            credentials = result["Credentials"]
            expiration = credentials["Expiration"]
        self.check(
            "grant_deadline",
            expiration <= requested_deadline,
            expires_at=expiration.isoformat(),
            requested_deadline=requested_deadline.isoformat(),
            requested_duration_seconds=self.args.duration,
        )
        client = self.session.client(
            "s3",
            endpoint_url=self.args.endpoint,
            config=self.config,
            aws_access_key_id=credentials["AccessKeyId"],
            aws_secret_access_key=credentials["SecretAccessKey"],
            aws_session_token=credentials["SessionToken"],
        )
        return client, expiration

    def head(self, name, key, size, checksum, checksum_type):
        result = self.service.head_object(
            Bucket=self.args.bucket, Key=key, ChecksumMode="ENABLED"
        )
        valid = (
            result["ContentLength"] == size
            and result.get("ChecksumSHA256") == checksum
            and result.get("ChecksumType") == checksum_type
        )
        self.record(
            name,
            "pass" if valid else "fail",
            size=result["ContentLength"],
            checksum_sha256=result.get("ChecksumSHA256"),
            checksum_type=result.get("ChecksumType"),
        )

    def multipart(self, client, key):
        result = client.create_multipart_upload(
            Bucket=self.args.bucket,
            Key=key,
            ChecksumAlgorithm="SHA256",
            ChecksumType="COMPOSITE",
        )
        upload_id = result["UploadId"]
        self.uploads.append((key, upload_id))
        data_parts = [b"a" * (5 * 1024 * 1024), b"artifact-contract-final-part"]
        parts = []
        for number, data in enumerate(data_parts, 1):
            checksum = digest(data)
            response = client.upload_part(
                Bucket=self.args.bucket,
                Key=key,
                UploadId=upload_id,
                PartNumber=number,
                Body=data,
                ChecksumSHA256=checksum,
            )
            self.record(
                f"multipart_part_{number}",
                "pass" if response.get("ChecksumSHA256") == checksum else "fail",
                returned_checksum=response.get("ChecksumSHA256"),
            )
            parts.append(
                {
                    "PartNumber": number,
                    "ETag": response["ETag"],
                    "ChecksumSHA256": checksum,
                }
            )
        listed = client.list_parts(Bucket=self.args.bucket, Key=key, UploadId=upload_id)
        self.check("list_parts", len(listed.get("Parts", [])) == 2)
        kwargs = {
            "Bucket": self.args.bucket,
            "Key": key,
            "UploadId": upload_id,
            "MultipartUpload": {"Parts": parts},
        }
        composite = (
            digest(b"".join(hashlib.sha256(data).digest() for data in data_parts))
            + "-2"
        )
        return kwargs, sum(map(len, data_parts)), composite

    def versioning_configuration(self):
        bucket = self.args.bucket
        versioning = self.service.get_bucket_versioning(Bucket=bucket).get(
            "Status", "Disabled"
        )
        self.record("versioning_configuration", "pass", versioning=versioning)
        # AWS versioned/suspended buckets retain versions; this MVP requires disabled versioning.
        self.check("versioning_reclamation_supported", versioning == "Disabled")

    def lifecycle_configuration(self):
        bucket = self.args.bucket
        lifecycle = self.service.get_bucket_lifecycle_configuration(Bucket=bucket)
        matching = []
        for rule in lifecycle.get("Rules", []):
            filter_ = rule.get("Filter", {})
            prefix = filter_.get("Prefix", rule.get("Prefix", ""))
            days = rule.get("AbortIncompleteMultipartUpload", {}).get(
                "DaysAfterInitiation"
            )
            if (
                rule.get("Status") == "Enabled"
                and set(filter_) <= {"Prefix"}
                and self.prefix.startswith(prefix)
                and isinstance(days, int)
                and days * 86400 > self.args.session_seconds
            ):
                matching.append(days)
        self.check(
            "multipart_lifecycle_configuration", bool(matching), abort_days=matching
        )

    def cors_configuration(self):
        bucket = self.args.bucket
        if self.args.origin:
            cors = self.service.get_bucket_cors(Bucket=bucket)
            required_methods = {"PUT", "POST", "GET", "DELETE", "HEAD"}
            required_headers = {
                "authorization",
                "content-type",
                "x-amz-date",
                "x-amz-security-token",
                "x-amz-content-sha256",
                "x-amz-checksum-sha256",
                "x-amz-checksum-algorithm",
                "x-amz-checksum-type",
                "x-amz-sdk-checksum-algorithm",
            }
            from fnmatch import fnmatchcase

            valid = False
            for rule in cors.get("CORSRules", []):
                allowed = [header.lower() for header in rule.get("AllowedHeaders", [])]
                exposed = {header.lower() for header in rule.get("ExposeHeaders", [])}
                valid |= (
                    any(
                        fnmatchcase(self.args.origin, origin)
                        for origin in rule["AllowedOrigins"]
                    )
                    and required_methods <= set(rule["AllowedMethods"])
                    and all(
                        any(fnmatchcase(header, pattern) for pattern in allowed)
                        for header in required_headers
                    )
                    and {"etag", "x-amz-checksum-sha256", "x-amz-checksum-type"}
                    <= exposed
                )
            self.check("cors_configuration", valid, origin=self.args.origin)
        else:
            self.record(
                "cors_configuration",
                "pending",
                reason="supply --origin for browser deployment",
            )

    def single_upload(self):
        bucket = self.args.bucket
        key = self.key("single")
        client, expiration = self.grant(key)
        data = b"artifact-go single object contract"
        put = {
            "Bucket": bucket,
            "Key": key,
            "Body": data,
            "ChecksumAlgorithm": "SHA256",
            "ChecksumSHA256": digest(data),
        }
        self.expiring_grant = (client, expiration, put)
        client.put_object(**put)
        self.record("single_upload", "pass")
        self.head(
            "single_trusted_checksum", key, len(data), digest(data), "FULL_OBJECT"
        )
        denied = {"AccessDenied", "403"}
        self.reject(
            "other_key_denied",
            lambda: client.put_object(**{**put, "Key": self.key("other")}),
            denied,
        )
        self.reject(
            "object_delete_denied",
            lambda: client.delete_object(Bucket=bucket, Key=key),
            denied,
        )
        self.reject(
            "bucket_policy_read_denied",
            lambda: client.get_bucket_policy(Bucket=bucket),
            denied,
        )
        self.record(
            "bucket_policy_modification",
            "pending",
            reason="session policy grants no bucket actions; no remote policy mutation attempted",
        )
        body = self.service.get_object(Bucket=bucket, Key=key)["Body"]
        try:
            self.check("stream_read", body.read(8) == data[:8])
        finally:
            body.close()
        self.check("stream_early_close", body._raw_stream.closed)
        self.record(
            "stream_cancellation",
            "pending",
            reason="Go adapter context cancellation requires adapter test",
        )

        bad_key = self.key("bad-checksum")
        bad_client, _ = self.grant(bad_key)
        self.reject(
            "invalid_checksum_denied",
            lambda: bad_client.put_object(
                **{**put, "Key": bad_key, "ChecksumSHA256": digest(b"wrong")}
            ),
            {"BadDigest"},
        )

    def multipart_upload(self):
        bucket = self.args.bucket
        multi_key = self.key("multipart")
        multi_client, _ = self.grant(multi_key)
        kwargs, size, checksum = self.multipart(multi_client, multi_key)
        multi_client.complete_multipart_upload(**kwargs)
        self.record("multipart_complete", "pass")
        self.head("multipart_trusted_checksum", multi_key, size, checksum, "COMPOSITE")
        result = multi_client.create_multipart_upload(
            Bucket=bucket,
            Key=multi_key,
            ChecksumAlgorithm="SHA256",
            ChecksumType="COMPOSITE",
        )
        self.uploads.append((multi_key, result["UploadId"]))
        multi_client.abort_multipart_upload(
            Bucket=bucket, Key=multi_key, UploadId=result["UploadId"]
        )
        self.record("multipart_abort", "pass")
        self.head(
            "multipart_abort_preserves_object", multi_key, size, checksum, "COMPOSITE"
        )

    def tenant_isolation(self):
        bucket = self.args.other_tenant_bucket
        if not bucket or self.args.service_only:
            self.record(
                "other_tenant_bucket_denied",
                "pending",
                reason="requires --other-tenant-bucket and restricted STS credentials",
            )
            return
        # Both buckets must be dedicated test buckets. Avoid retaining historical versions.
        versioning = self.service.get_bucket_versioning(Bucket=bucket).get(
            "Status", "Disabled"
        )
        self.check("other_tenant_versioning_disabled", versioning == "Disabled")
        key = self.key("tenant-isolation")
        self.keys.add((bucket, key))
        client, _ = self.grant(key)
        data = b"artifact-go tenant isolation positive control"
        client.put_object(Bucket=self.args.bucket, Key=key, Body=data)
        self.service.put_object(Bucket=bucket, Key=key, Body=data)
        self.record("tenant_buckets_positive_control", "pass")
        self.reject(
            "other_tenant_bucket_denied",
            lambda: client.put_object(Bucket=bucket, Key=key, Body=data),
            {"AccessDenied", "403"},
        )

    def delete_and_expiration(self):
        bucket = self.args.bucket
        missing = self.key("missing")
        self.service.delete_object(Bucket=bucket, Key=missing)
        self.service.delete_object(Bucket=bucket, Key=missing)
        self.record("delete_missing_idempotent", "pass")
        if not hasattr(self, "expiring_grant"):
            self.record(
                "credential_expiration",
                "pending",
                reason="single upload prerequisite failed",
            )
            return
        client, expiration, put = self.expiring_grant
        if expiration is None:
            self.record(
                "credential_expiration",
                "pending",
                reason="service-only mode; STS not used",
            )
            return
        if self.args.wait_expiry:
            self.record(
                "credential_expiration_wait",
                "pending",
                expires_at=expiration.isoformat(),
            )
            self.report()
            print(
                "Waiting for actual credential expiry; no credentials are logged.",
                flush=True,
            )
            while datetime.now(UTC) <= expiration + timedelta(seconds=5):
                time.sleep(10)
            try:
                client.put_object(**put)
            except ClientError as error:
                code = error.response["Error"]["Code"]
                message = error.response["Error"].get("Message", "").lower()
                expiration_reason = any(
                    phrase in message
                    for phrase in (
                        "token is expired",
                        "token has expired",
                        "expired token",
                        "expiredsignature",
                        "credentials have expired",
                        "credentials are expired",
                    )
                )
                self.record(
                    "credential_expiration",
                    "pass"
                    if code == "ExpiredToken" or expiration_reason
                    else "pending",
                    error_code=code,
                    request_rejected=True,
                    reason_classification="token_expired"
                    if code == "ExpiredToken" or expiration_reason
                    else "unclassified_rejection",
                )
            else:
                self.record(
                    "credential_expiration",
                    "fail",
                    reason="expired_credentials_accepted",
                )
        else:
            self.record(
                "credential_expiration",
                "pending",
                expires_at=expiration.isoformat(),
                reason="rerun with --wait-expiry for actual expiration test",
            )

    def run_group(self, name, action):
        try:
            action()
        except Exception as error:  # noqa: BLE001 - isolate capability failures and redact errors
            code = (
                error.response["Error"]["Code"]
                if isinstance(error, ClientError)
                else type(error).__name__
            )
            self.record(name, "fail", error_code=code)
            if isinstance(error, HTTPError):
                details = {"http_status": error.code}
                try:
                    fields = {
                        e.tag.rsplit("}", 1)[-1]: e.text
                        for e in ET.fromstring(error.read()).iter()
                    }
                    details["storage_error_code"] = fields.get("Code")
                except ET.ParseError:
                    pass
                self.record(name + "_http", "fail", **details)

    def run(self):
        self.run_group("versioning_configuration_group", self.versioning_configuration)
        self.run_group("lifecycle_configuration_group", self.lifecycle_configuration)
        self.run_group("cors_configuration_group", self.cors_configuration)
        self.record(
            "multipart_lifecycle_execution",
            "pending",
            reason="observe actual scheduled reclamation",
        )
        self.record(
            "browser_direct_upload",
            "pending",
            reason="run from deployment browser origin",
        )
        # Never leave historical versions from an unsupported versioned deployment.
        if any(
            check["name"] == "versioning_reclamation_supported"
            and check["status"] == "fail"
            for check in self.checks
        ):
            self.record(
                "uploads",
                "pending",
                reason="versioning must be disabled before disposable probes",
            )
            return
        self.run_group("tenant_isolation_group", self.tenant_isolation)
        self.run_group("single_upload_group", self.single_upload)
        self.run_group("multipart_upload_group", self.multipart_upload)
        self.run_group("delete_and_expiration_group", self.delete_and_expiration)

    def cleanup(self):
        for key, upload_id in self.uploads:
            try:
                self.service.abort_multipart_upload(
                    Bucket=self.args.bucket, Key=key, UploadId=upload_id
                )
            except ClientError as error:
                if error.response["Error"]["Code"] != "NoSuchUpload":
                    self.record(
                        "cleanup_multipart",
                        "fail",
                        error_code=error.response["Error"]["Code"],
                    )
        for bucket, key in sorted(self.keys):
            self.service.delete_object(Bucket=bucket, Key=key)
            self.reject(
                "cleanup_object_absent",
                lambda bucket=bucket, key=key: self.service.head_object(
                    Bucket=bucket, Key=key
                ),
                {"404", "NoSuchKey", "NotFound"},
            )

    def report(self):
        executed = {check["name"] for check in self.checks}
        required = (
            "single_upload",
            "single_trusted_checksum",
            "other_key_denied",
            "object_delete_denied",
            "bucket_policy_read_denied",
            "bucket_policy_modification",
            "stream_read",
            "stream_early_close",
            "stream_cancellation",
            "invalid_checksum_denied",
            "multipart_complete",
            "multipart_trusted_checksum",
            "multipart_abort",
            "multipart_abort_preserves_object",
            "other_tenant_bucket_denied",
            "delete_missing_idempotent",
            "credential_expiration",
            "multipart_lifecycle_execution",
            "browser_direct_upload",
            "cors_configuration",
        )
        for name in required:
            if name not in executed:
                self.record(
                    name,
                    "pending",
                    reason="prerequisite failed or capability not executed",
                )
        statuses = {check["status"] for check in self.checks}
        status = (
            "fail"
            if "fail" in statuses
            else "incomplete"
            if "pending" in statuses
            else "pass"
        )
        result = {
            "schema_version": 2,
            "contract": "tenant-bucket-client-managed-content",
            "status": status,
            "run_id": self.run_id,
            "backend": self.args.backend,
            "service_only": self.args.service_only,
            "bucket": self.args.bucket,
            "other_tenant_bucket": self.args.other_tenant_bucket,
            "prefix": self.prefix,
            "s3_endpoint": self.service.meta.endpoint_url,
            "sts_endpoint": (self.args.sts_endpoint or self.args.endpoint)
            if self.args.backend in {"rustfs", "juicefs"}
            else self.sts.meta.endpoint_url,
            "region": self.args.region,
            "finished_at": datetime.now(UTC).isoformat(),
            "deployment_limits": {
                "grant_seconds": self.args.duration,
                "session_seconds": self.args.session_seconds,
                "max_file_bytes": self.args.max_file_bytes,
                "max_files_per_session": self.args.max_files,
            },
            "checks": self.checks,
        }
        self.args.report.write_text(json.dumps(result, indent=2) + "\n")
        return 1 if status == "fail" else 2 if status == "incomplete" else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bucket", required=True)
    parser.add_argument(
        "--other-tenant-bucket",
        help="second dedicated tenant test bucket on the same endpoint for isolation checks",
    )
    parser.add_argument(
        "--backend", choices=["aws", "rustfs", "juicefs"], default="rustfs"
    )
    parser.add_argument(
        "--role-arn", help="required for AWS: upload role assumed by service identity"
    )
    parser.add_argument("--prefix", default="artifact-contract")
    parser.add_argument("--region", required=True)
    parser.add_argument("--profile")
    parser.add_argument(
        "--grant-profile", help="separate long-term IAM user profile for STS issuance"
    )
    parser.add_argument("--endpoint")
    parser.add_argument("--sts-endpoint")
    parser.add_argument(
        "--addressing-style", choices=["auto", "path", "virtual"], default="auto"
    )
    parser.add_argument("--duration", type=int, default=900)
    parser.add_argument("--session-seconds", type=int, default=3600)
    parser.add_argument("--max-file-bytes", type=int, default=1073741824)
    parser.add_argument("--max-files", type=int, default=100)
    parser.add_argument("--origin")
    parser.add_argument("--wait-expiry", action="store_true")
    parser.add_argument(
        "--service-only",
        action="store_true",
        help="test data APIs using existing service identity; grant isolation remains pending",
    )
    parser.add_argument("--report", type=Path, default=Path("s3-contract-report.json"))
    args = parser.parse_args()
    if args.other_tenant_bucket == args.bucket:
        parser.error("--other-tenant-bucket must differ from --bucket")
    if args.backend == "aws" and not args.role_arn:
        parser.error("--role-arn is required for AWS")
    if args.backend in {"rustfs", "juicefs"} and not args.endpoint:
        parser.error("--endpoint is required for RustFS/JuiceFS")
    for endpoint in (args.endpoint, args.sts_endpoint):
        if endpoint and (
            urlparse(endpoint).username
            or urlparse(endpoint).password
            or urlparse(endpoint).query
            or urlparse(endpoint).fragment
        ):
            parser.error(
                "endpoints must not contain credentials, query parameters or fragments"
            )
    if not 900 <= args.duration <= 3600 or args.duration + 30 > args.session_seconds:
        parser.error(
            "duration must be 900..3600 seconds with >=30 seconds before session deadline"
        )
    if not args.prefix.strip("/") or "*" in args.prefix or "?" in args.prefix:
        parser.error("prefix must be a dedicated nonempty prefix without IAM wildcards")
    if args.max_file_bytes < 5 * 1024 * 1024 + 28 or args.max_files < 2:
        parser.error(
            "test deployment limits must allow the multipart fixture and two files"
        )
    probe = Probe(args)
    try:
        probe.run()
    except Exception as error:  # noqa: BLE001 - retain a sanitized report for any probe failure
        # Error messages or HTTP bodies may contain sensitive request data; only report safe codes.
        code = (
            error.response["Error"]["Code"]
            if isinstance(error, ClientError)
            else type(error).__name__
        )
        probe.record("run", "fail", error_code=code)
    finally:
        try:
            probe.cleanup()
        except Exception as error:  # noqa: BLE001 - report cleanup failure without leaking request data
            code = (
                error.response["Error"]["Code"]
                if isinstance(error, ClientError)
                else type(error).__name__
            )
            probe.record(
                "cleanup",
                "fail",
                error_code=code,
                reason="inspect only the recorded UUID prefix",
            )
    return probe.report()


if __name__ == "__main__":
    sys.exit(main())
