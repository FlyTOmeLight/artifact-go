#!/usr/bin/env python3
"""Freeze and restore two business databases plus isolated JuiceFS metadata/objects."""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
JFS_IMAGE = "artifact-juicefs:1.4.1-arm64"


def main():
    tag = "artifact-four-" + uuid.uuid4().hex[:12]
    containers, volumes = [], []
    network_created = False
    env = {**os.environ, "GOWORK": "off", "GIN_MODE": "release"}

    def run(*args, capture=False, **kwargs):
        return subprocess.run(args, cwd=ROOT, env=env, check=True,
                              stdout=kwargs.pop("stdout", subprocess.PIPE if capture else None),
                              **kwargs).stdout

    def wait(args):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                return
            time.sleep(0.25)
        raise RuntimeError("disposable four-layer service readiness timed out")

    def start(name, image, *args, port=None, extra=()):
        full = tag + "-" + name
        containers.append(full)
        cmd = ["docker", "run", "--pull=never", "-d", "--name", full, "--network", tag]
        if port:
            cmd += ["-p", f"127.0.0.1::{port}"]
        run(*cmd, *extra, image, *args, capture=True)
        return full

    def mapped_port(name, port):
        return run("docker", "port", name, port, capture=True).decode().strip().rsplit(":", 1)[1]

    try:
        run("docker", "network", "create", tag, capture=True)
        network_created = True
        with tempfile.TemporaryDirectory(prefix=tag + "-") as temp:
            work = Path(temp)
            # Only credentials generated for this run; no cluster configuration is loaded.
            password = secrets.token_urlsafe(32)
            env["MINIO_ROOT_USER"] = "four-layer"
            env["MINIO_ROOT_PASSWORD"] = secrets.token_urlsafe(32)
            private_env = work / "private.env"
            fd = os.open(private_env, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd, "w") as out:
                out.write(f"MINIO_ROOT_USER={env['MINIO_ROOT_USER']}\nMINIO_ROOT_PASSWORD={env['MINIO_ROOT_PASSWORD']}\nPOSTGRES_PASSWORD={password}\n")
            env["ARTIFACT_FOUR_LAYER_STATE"] = str(work / "state.json")
            source, restored, backup = (tag + suffix for suffix in ("-source", "-restored", "-backup"))
            for name in (source, restored, backup):
                volumes.append(name)
                run("docker", "volume", "create", name, capture=True)

            def postgres(name):
                instance = start(name, "postgres:17-alpine", port="5432", extra=("--env-file", str(private_env)))
                wait(["docker", "exec", instance, "pg_isready", "-U", "postgres"])
                for database in ("artifact", "storage"):
                    run("docker", "exec", instance, "createdb", "-U", "postgres", database, capture=True)
                return instance

            def use_postgres(instance):
                port = mapped_port(instance, "5432")
                for domain in ("artifact", "storage"):
                    env[f"ARTIFACT_FOUR_LAYER_{domain.upper()}_DSN"] = f"postgres://postgres:{password}@127.0.0.1:{port}/{domain}?sslmode=disable"

            pg_source = postgres("pg-source")
            use_postgres(pg_source)
            redis_source = start("redis-source", "redis:7.4.2-alpine", "redis-server", "--save", "", "--appendonly", "no")
            wait(["docker", "exec", redis_source, "redis-cli", "ping"])
            src_meta = f"redis://{redis_source}:6379/1"

            def jfs(*args, data_volume=source):
                run("docker", "run", "--rm", "--pull=never", "--network", tag,
                    "-v", backup + ":/backup", "-v", data_volume + ":/data", JFS_IMAGE, *args,
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

            jfs("format", "--storage", "file", "--bucket", "/data", "--capacity", "1", "--trash-days", "1", src_meta, "four-layer")

            def gateway(name, meta, volume):
                instance = start(name, JFS_IMAGE, "gateway", "--multi-buckets", "--keep-etag", "--object-meta", "--no-usage-report", "--no-banner", "--backup-meta", "0", "--cache-size", "0", "--buffer-size", "32", meta, "0.0.0.0:9010", port="9010", extra=("--env-file", str(private_env), "-v", volume + ":/data"))
                env["ARTIFACT_FOUR_LAYER_S3"] = f"http://127.0.0.1:{mapped_port(instance, '9010')}"
                wait(["curl", "--silent", "--fail", env["ARTIFACT_FOUR_LAYER_S3"] + "/minio/health/live"])
                return instance

            gateway_source = gateway("gateway-source", src_meta, source)
            env["ARTIFACT_FOUR_LAYER_PHASE"] = "seed"
            run("go", "test", "-race", "-count=1", "-timeout=3m", "-v", "./deploy/acceptance", "-run", "^TestFourLayerRecovery$")
            # Successful go test exit joins all HTTP handlers, workers and DB cleanup.
            run("docker", "stop", "-t", "20", gateway_source, capture=True)
            frozen = time.monotonic()
            for database in ("artifact", "storage"):
                dump = work / f"{database}.dump"
                with dump.open("wb") as out:
                    run("docker", "exec", pg_source, "pg_dump", "-U", "postgres", "--format=custom", "--no-owner", "--no-privileges", database, stdout=out, stderr=subprocess.DEVNULL)
                dump.chmod(0o600)
                if dump.stat().st_size == 0:
                    raise RuntimeError("empty business database backup")
            jfs("dump", src_meta, "/backup/meta.json")
            run("docker", "run", "--rm", "--pull=never", "--network", "none", "-v", source + ":/source:ro", "-v", backup + ":/backup", "busybox:1.37.0", "sh", "-ec", "mkdir /backup/data; cp -a /source/. /backup/data/", capture=True)
            print(f"FOUR-LAYER frozen paired DB + JuiceFS metadata + object backup seconds={time.monotonic() - frozen:.3f}", flush=True)
            # Prove recovery cannot contact source services or their memory/cache.
            run("docker", "stop", "-t", "20", pg_source, redis_source, capture=True)
            recovery = time.monotonic()
            pg_restored = postgres("pg-restored")
            for database in ("artifact", "storage"):
                with (work / f"{database}.dump").open("rb") as data:
                    run("docker", "exec", "-i", pg_restored, "pg_restore", "-U", "postgres", "--no-owner", "--no-privileges", "-d", database, stdin=data, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            use_postgres(pg_restored)
            redis_restored = start("redis-restored", "redis:7.4.2-alpine", "redis-server", "--save", "", "--appendonly", "no")
            wait(["docker", "exec", redis_restored, "redis-cli", "ping"])
            dst_meta = f"redis://{redis_restored}:6379/1"
            run("docker", "run", "--rm", "--pull=never", "--network", "none", "-v", restored + ":/restored", "-v", backup + ":/backup:ro", "busybox:1.37.0", "cp", "-a", "/backup/data/.", "/restored/", capture=True)
            jfs("load", dst_meta, "/backup/meta.json", data_volume=restored)
            gateway("gateway-restored", dst_meta, restored)
            env["ARTIFACT_FOUR_LAYER_PHASE"] = "restore"
            run("go", "test", "-race", "-count=1", "-timeout=3m", "-v", "./deploy/acceptance", "-run", "^TestFourLayerRecovery$")
            print(f"FOUR-LAYER recovery + original bytes + resumed obligations seconds={time.monotonic() - recovery:.3f}", flush=True)
            print("PASS: isolated four-layer frozen recovery; source services stopped; retained shared version/reference and physical unique deletion verified; not production RPO/RTO, Forge login or STS", flush=True)
    finally:
        for name in reversed(containers):
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for name in reversed(volumes):
            subprocess.run(["docker", "volume", "rm", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if network_created:
            subprocess.run(["docker", "network", "rm", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
