#!/usr/bin/env python3
"""Offline OrbStack drill; creates and removes only its UUID-named resources."""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "artifact-juicefs:1.4.1-arm64"


def main():
    tag = "artifact-drill-" + uuid.uuid4().hex[:12]
    containers, volumes = [], []
    network = False
    env = {**os.environ, "GOWORK": "off", "ARTIFACT_DEPLOY_ACCEPTANCE": "1"}

    def run(*args, capture=False, **kwargs):
        return subprocess.run(args, cwd=ROOT, env=env, check=True, text=True,
                              stdout=kwargs.pop("stdout", subprocess.PIPE if capture else None), **kwargs).stdout

    def start(name, image, *args, port=None, extra=()):
        full = tag + "-" + name
        command = ["docker", "run", "--pull=never", "-d", "--name", full, "--network", tag]
        if port:
            command += ["-p", f"127.0.0.1::{port}"]
        containers.append(full)
        run(*command, *extra, image, *args, capture=True)
        return full

    def wait(command):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if subprocess.run(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                return
            time.sleep(0.25)
        raise RuntimeError("isolated service readiness timed out")

    try:
        run("docker", "network", "create", tag, capture=True)
        network = True
        with tempfile.TemporaryDirectory(prefix=tag + "-") as temp:
            work = Path(temp)
            secret_file = work / "gateway.env"
            env["MINIO_ROOT_USER"] = "acceptance"
            env["MINIO_ROOT_PASSWORD"] = secrets.token_urlsafe(32)
            secret_file.write_text("MINIO_ROOT_USER=" + env["MINIO_ROOT_USER"] + "\nMINIO_ROOT_PASSWORD=" + env["MINIO_ROOT_PASSWORD"] + "\n")
            secret_file.chmod(0o600)
            source, restored, backup = (tag + suffix for suffix in ("-source", "-restored", "-backup"))
            for volume in (source, restored, backup):
                run("docker", "volume", "create", volume, capture=True)
                volumes.append(volume)
            pg = start("pg", "postgres:17-alpine", port="5432", extra=("-e", "POSTGRES_PASSWORD=acceptance-only", "-e", "POSTGRES_DB=acceptance"))
            wait(["docker", "exec", pg, "pg_isready", "-U", "postgres"])
            pg_port = run("docker", "port", pg, "5432", capture=True).strip().rsplit(":", 1)[1]
            env["ARTIFACT_TEST_PG_DSN"] = f"postgres://postgres:acceptance-only@127.0.0.1:{pg_port}/acceptance?sslmode=disable"
            run("go", "test", "-race", "-count=1", "-v", "./deploy/acceptance", "-run", "^TestTLSRotation$")
            redis_src = start("redis-source", "redis:7.4.2-alpine", "redis-server", "--save", "", "--appendonly", "no")
            redis_dst = start("redis-restored", "redis:7.4.2-alpine", "redis-server", "--save", "", "--appendonly", "no")
            for redis in (redis_src, redis_dst):
                wait(["docker", "exec", redis, "redis-cli", "ping"])
            src_meta, dst_meta = (f"redis://{name}:6379/1" for name in (redis_src, redis_dst))

            def jfs(*args):
                run("docker", "run", "--rm", "--pull=never", "--network", tag,
                    "-v", backup + ":/backup", "-v", source + ":/data", IMAGE, *args,
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

            jfs("format", "--storage", "file", "--bucket", "/data", "--capacity", "1", "--trash-days", "1", src_meta, "acceptance")

            def gateway(name, meta, volume):
                full = start(name, IMAGE, "gateway", "--multi-buckets", "--keep-etag", "--object-meta", "--no-usage-report", "--no-banner", "--backup-meta", "0", "--cache-size", "0", "--buffer-size", "32", meta, "0.0.0.0:9010", port="9010", extra=("--env-file", str(secret_file), "-v", volume + ":/data"))
                port = run("docker", "port", full, "9010", capture=True).strip().rsplit(":", 1)[1]
                env["ARTIFACT_ACCEPTANCE_S3"] = f"http://127.0.0.1:{port}"
                wait(["curl", "--silent", "--fail", env["ARTIFACT_ACCEPTANCE_S3"] + "/minio/health/live"])
                return full

            source_gateway = gateway("gateway-source", src_meta, source)
            env["ARTIFACT_ACCEPTANCE_WRITE"] = "1"
            run("go", "test", "-count=1", "-v", "./deploy/acceptance", "-run", "^TestJuiceFSBytes$")
            run("docker", "stop", "-t", "20", source_gateway, capture=True)
            frozen = time.monotonic()
            jfs("dump", src_meta, "/backup/meta.json")
            run("docker", "run", "--rm", "--pull=never", "--network", "none", "-v", source + ":/source:ro", "-v", backup + ":/backup", "busybox:1.37.0", "sh", "-ec", "mkdir /backup/data; cp -a /source/. /backup/data/", capture=True)
            print(f"BACKUP frozen metadata+object copy seconds={time.monotonic() - frozen:.3f}", flush=True)
            recovery = time.monotonic()
            run("docker", "run", "--rm", "--pull=never", "--network", "none", "-v", restored + ":/restored", "-v", backup + ":/backup:ro", "busybox:1.37.0", "cp", "-a", "/backup/data/.", "/restored/", capture=True)
            jfs("load", dst_meta, "/backup/meta.json")
            gateway("gateway-restored", dst_meta, restored)
            env["ARTIFACT_ACCEPTANCE_WRITE"] = "0"
            run("go", "test", "-count=1", "-v", "./deploy/acceptance", "-run", "^TestJuiceFSBytes$")
            print(f"RECOVERY copied objects+fresh Redis+fresh cache+byte verification seconds={time.monotonic() - recovery:.3f}", flush=True)
            print("PASS: isolated TLS/rotation and frozen JuiceFS metadata+object recovery; 8 MiB, concurrency 4; not production capacity or RPO/RTO", flush=True)
    finally:
        for name in reversed(containers):
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for name in reversed(volumes):
            subprocess.run(["docker", "volume", "rm", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if network:
            subprocess.run(["docker", "network", "rm", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
