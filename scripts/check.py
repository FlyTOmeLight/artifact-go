#!/usr/bin/env python3
"""Run artifact checks against disposable PostgreSQL and MySQL containers."""
import os
from pathlib import Path
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
ENV = {**os.environ, "GOWORK": "off", "CGO_ENABLED": "1"}


def run(*args, capture=False):
    return subprocess.run(args, cwd=ROOT, env=ENV, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def wait_ready(args):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        result = subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if result.returncode == 0:
            return
        time.sleep(0.5)
    raise RuntimeError("database failed readiness check")


def main():
    run("docker", "version", "--format", "{{.Server.Version}}")
    containers = []
    try:
        suffix = uuid.uuid4().hex[:10]
        for kind, image, port, settings in (
            ("pg", "postgres:17-alpine", "5432", ["POSTGRES_PASSWORD=artifact-test-only", "POSTGRES_DB=artifact_test"]),
            ("mysql", "mysql:8.4", "3306", ["MYSQL_ROOT_PASSWORD=artifact-test-only", "MYSQL_ROOT_HOST=%", "MYSQL_DATABASE=artifact_test"]),
        ):
            command = ["docker", "run", "--rm", "-d", "--name", f"artifact-t01-{kind}-{suffix}", "-p", f"127.0.0.1::{port}"]
            for setting in settings:
                command.extend(["-e", setting])
            command.append(image)
            cid = run(*command, capture=True).strip()
            containers.append(cid)
            ENV["ARTIFACT_TEST_" + kind.upper() + "_CONTAINER"] = cid
            if kind == "pg":
                wait_ready(["docker", "exec", cid, "pg_isready", "-U", "postgres"])
            else:
                wait_ready(["docker", "exec", cid, "mysqladmin", "ping", "-h", "127.0.0.1", "--silent"])
            address = run("docker", "port", cid, port, capture=True).strip().splitlines()[0]
            public_port = address.rsplit(":", 1)[1]
            if kind == "pg":
                ENV["ARTIFACT_TEST_PG_DSN"] = f"postgres://postgres:artifact-test-only@127.0.0.1:{public_port}/artifact_test?sslmode=disable"
            else:
                ENV["ARTIFACT_TEST_MYSQL_PORT"] = public_port
        run("go", "test", "-count=1", "-race", "-timeout=5m", "./...")
        run("go", "vet", "./...")
        run("go", "build", "-o", "bin/artifact-server", "./cmd/server")
        run("go", "build", "-o", "bin/artifact-forge-server", "./cmd/forge-server")
        run("go", "build", "-o", "bin/artifact-maintenance", "./cmd/maintenance")
        run("go", "build", "-o", "bin/artifact-migrate", "./cmd/migrate")
        run("go", "-C", "cmd/storage-bootstrap", "test", "-count=1", "-race", "-timeout=5m", "./...")
        run("go", "-C", "cmd/storage-bootstrap", "vet", "./...")
        run("go", "-C", "cmd/storage-bootstrap", "build", "-o", "../../bin/storage-bootstrap", ".")
        print("PASS: HTTP/SDK and database checks, PostgreSQL + MySQL, paired database backup/restore, lifecycle, race, vet, build, migration and storage-bootstrap CLI checks; external identity/JuiceFS probes reported separately")
    finally:
        for cid in reversed(containers):
            subprocess.run(["docker", "rm", "-f", cid], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
