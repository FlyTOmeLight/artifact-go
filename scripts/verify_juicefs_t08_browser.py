# /// script
# requires-python = ">=3.11"
# dependencies = ["boto3==1.43.113", "playwright==1.58.0"]
# ///
"""Exercise actual browser CORS with ordinary-IAM STS and signed fetch requests."""

import hashlib
import json
import os
import secrets
import threading
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlencode
from urllib.request import Request, urlopen
import boto3
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from botocore.config import Config
from playwright.sync_api import sync_playwright

endpoint = os.environ["ARTIFACT_TEST_S3_ENDPOINT"]
bucket = "tenant-t-" + secrets.token_hex(12)
config = Config(
    signature_version="s3v4",
    s3={"addressing_style": "path"},
    retries={"total_max_attempts": 1},
)
operator = boto3.client(
    "s3",
    endpoint_url=endpoint,
    region_name="us-east-1",
    aws_access_key_id=os.environ["ARTIFACT_TEST_S3_ACCESS_KEY_ID"],
    aws_secret_access_key=os.environ["ARTIFACT_TEST_S3_SECRET_ACCESS_KEY"],
    config=config,
)


def issue(action):
    policy = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Action": [action],
                "Resource": [f"arn:aws:s3:::{bucket}/browser"],
            }
        ],
    }
    body = urlencode(
        {
            "Action": "AssumeRole",
            "Version": "2011-06-15",
            "DurationSeconds": 900,
            "Policy": json.dumps(policy),
        }
    )
    req = AWSRequest(
        method="POST",
        url=endpoint,
        data=body,
        headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "x-amz-content-sha256": hashlib.sha256(body.encode()).hexdigest(),
        },
    )
    SigV4Auth(
        Credentials(
            os.environ["ARTIFACT_TEST_STS_ACCESS_KEY_ID"],
            os.environ["ARTIFACT_TEST_STS_SECRET_ACCESS_KEY"],
        ),
        "sts",
        "us-east-1",
    ).add_auth(req)
    with urlopen(
        Request(endpoint, data=body.encode(), headers=dict(req.headers)), timeout=10
    ) as response:
        result = {
            e.tag.rsplit("}", 1)[-1]: e.text
            for e in ET.fromstring(response.read()).iter()
        }
    return Credentials(
        result["AccessKeyId"], result["SecretAccessKey"], result["SessionToken"]
    )


class Origin(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"<html>T08 CORS</html>")

    def log_message(self, *args):
        pass


server = ThreadingHTTPServer(("127.0.0.1", 0), Origin)
threading.Thread(target=server.serve_forever, daemon=True).start()
created = False
try:
    operator.create_bucket(Bucket=bucket)
    created = True
    upload, download = issue("s3:PutObject"), issue("s3:GetObject")
    with sync_playwright() as pw:
        browser = pw.chromium.launch(
            executable_path=os.environ.get("ARTIFACT_TEST_BROWSER_EXECUTABLE"),
            headless=True,
        )
        page = browser.new_page()
        page.goto(f"http://127.0.0.1:{server.server_port}")
        for method, body, headers, credential in [
            ("PUT", "hello", {"content-type": "application/octet-stream"}, upload),
            ("GET", None, {}, download),
            ("HEAD", None, {}, download),
            ("GET", None, {"Range": "bytes=1-3"}, download),
        ]:
            req = AWSRequest(
                method=method,
                url=f"{endpoint}/{bucket}/browser",
                data=body,
                headers=headers,
            )
            req.headers["x-amz-content-sha256"] = hashlib.sha256(
                (body or "").encode()
            ).hexdigest()
            SigV4Auth(credential, "s3", "us-east-1").add_auth(req)
            data = {
                "url": req.url,
                "method": method,
                "headers": dict(req.headers),
                "body": body,
            }
            result = page.evaluate(
                """async r=>{const x=await fetch(r.url,{method:r.method,headers:r.headers,body:r.body});return {status:x.status,body:await x.text(),etag:x.headers.get('etag'),range:x.headers.get('content-range')}}""",
                data,
            )
            expected = 206 if "Range" in headers else 200
            if result["status"] != expected:
                raise RuntimeError("browser operation status mismatch")
            if method == "GET" and result["body"] != (
                "ell" if "Range" in headers else "hello"
            ):
                raise RuntimeError("browser body mismatch")
            if method == "PUT" and not result["etag"]:
                raise RuntimeError("ETag not exposed")
            print(
                method
                + (" Range" if "Range" in headers else "")
                + " browser fetch passed",
                flush=True,
            )
        browser.close()
finally:
    server.shutdown()
    server.server_close()
    if created:
        operator.delete_object(Bucket=bucket, Key="browser")
        operator.delete_bucket(Bucket=bucket)
