#!/usr/bin/env python3
"""
scripts/enqueue_import.py

Insert a CSV file into pending_imports, create a processing_jobs record,
and publish an import.process job to the veloci.jobs RabbitMQ queue.

Requires the management plugin to be active on the RabbitMQ instance
(docker-compose.dev.yml switches the image to rabbitmq:4-management-alpine).

Connection parameters are read from environment variables matching .env:
  VELOCI_APP_DB_USER, VELOCI_APP_DB_PASSWORD, VELOCI_APP_DB
  RABBITMQ_USER, RABBITMQ_PASSWORD

Usage (via Justfile):
  just enqueue-import path/to/file.csv <entity-uuid> <account-uuid>
"""

import base64
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
import uuid
from datetime import date


def die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def psql(sql: str, pg_user: str, pg_pass: str, pg_host: str, pg_port: str, pg_db: str) -> str:
    env = os.environ.copy()
    env["PGPASSWORD"] = pg_pass
    result = subprocess.run(
        [
            "psql",
            "-h", pg_host,
            "-p", pg_port,
            "-U", pg_user,
            "-d", pg_db,
            "-t",   # tuples only (no headers/footers)
            "-A",   # unaligned output
            "-q",   # quiet: suppress command-completion tags (INSERT 0 1, etc.)
            "-c", sql,
        ],
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        die(f"psql failed:\n{result.stderr.strip()}")
    return result.stdout.strip()


def mq_request(
    method: str,
    path: str,
    mq_host: str,
    mq_port: str,
    mq_user: str,
    mq_pass: str,
    body=None,
) -> dict:
    url = f"http://{mq_host}:{mq_port}{path}"
    data = json.dumps(body).encode() if body is not None else None
    creds = base64.b64encode(f"{mq_user}:{mq_pass}".encode()).decode()
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Basic {creds}",
        },
    )
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        die(f"RabbitMQ HTTP {e.code} on {method} {path}:\n{e.read().decode()}")
    except urllib.error.URLError as e:
        die(f"Cannot reach RabbitMQ at {mq_host}:{mq_port} — is dev-up running?\n{e.reason}")


def main() -> None:
    if len(sys.argv) != 4:
        print("Usage: enqueue_import.py <csv_file> <entity_id> <account_id>")
        sys.exit(1)

    csv_file   = sys.argv[1]
    entity_id  = sys.argv[2]
    account_id = sys.argv[3]

    # Validate UUIDs early
    for label, val in [("entity_id", entity_id), ("account_id", account_id)]:
        try:
            uuid.UUID(val)
        except ValueError:
            die(f"{label} is not a valid UUID: {val!r}")

    # Connection params from environment (loaded by Justfile via dotenv)
    pg_user = os.environ.get("VELOCI_APP_DB_USER", "veloci_app_user")
    pg_pass = os.environ.get("VELOCI_APP_DB_PASSWORD", "changeme_app")
    pg_host = os.environ.get("PG_HOST", "localhost")
    pg_port = os.environ.get("PG_PORT", "5432")
    pg_db   = os.environ.get("VELOCI_APP_DB", "veloci_app")
    mq_host = os.environ.get("MQ_HOST", "localhost")
    mq_port = os.environ.get("MQ_MGMT_PORT", "15672")
    mq_user = os.environ.get("RABBITMQ_USER", "veloci")
    mq_pass = os.environ.get("RABBITMQ_PASSWORD", "changeme")

    def db(sql: str) -> str:
        return psql(sql, pg_user, pg_pass, pg_host, pg_port, pg_db)

    def mq(method: str, path: str, body=None) -> dict:
        return mq_request(method, path, mq_host, mq_port, mq_user, mq_pass, body)

    # ── 1. Read CSV ────────────────────────────────────────────────────────────
    csv_path = os.path.abspath(csv_file)
    if not os.path.isfile(csv_path):
        die(f"CSV file not found: {csv_path}")

    with open(csv_path, "rb") as f:
        csv_bytes = f.read()

    # Base64-encode for safe transport through psql string literals
    csv_b64 = base64.b64encode(csv_bytes).decode("ascii")
    print(f"CSV: {csv_path} ({len(csv_bytes):,} bytes)")

    # ── 2. Resolve a user for this entity (for uploaded_by / triggered_by) ────
    user_id = db(
        f"SELECT eu.user_id FROM entity_users eu WHERE eu.entity_id = '{entity_id}' ORDER BY eu.user_id LIMIT 1"
    )
    if not user_id:
        die(
            f"No users found for entity {entity_id}.\n"
            "Create at least one user via the API before enqueuing an import."
        )

    # ── 2b. Resolve institution_id from the account ───────────────────────────
    institution_id = db(
        f"SELECT institution_id FROM accounts WHERE id = '{account_id}' AND entity_id = '{entity_id}'"
    )
    if not institution_id:
        die(
            f"Account {account_id} not found or has no institution mapping.\n"
            "Run: just dev-seed — then verify the account has an institution_id set."
        )

    # ── 3. Create processing_jobs record ──────────────────────────────────────
    job_id = str(uuid.uuid4())
    today  = date.today().isoformat()
    print(f"Creating processing_jobs record... job_id={job_id}")
    db(f"""
        INSERT INTO processing_jobs (id, entity_id, job_type, triggered_by, status, queued_at)
        VALUES ('{job_id}', '{entity_id}', 'import.process', '{user_id}', 'queued', NOW())
    """)

    # ── 4. Insert CSV into pending_imports ────────────────────────────────────
    print("Inserting CSV into pending_imports...")
    import_id = db(f"""
        INSERT INTO pending_imports
          (entity_id, account_id, institution_id, uploaded_by, uploaded_at,
           csv_bytes, date_range_start, date_range_end, status, job_id)
        VALUES (
          '{entity_id}',
          '{account_id}',
          '{institution_id}',
          '{user_id}',
          NOW(),
          decode('{csv_b64}', 'base64'),
          '2020-01-01',
          '{today}',
          'pending',
          '{job_id}'
        )
        RETURNING id
    """)
    if not import_id:
        die("INSERT into pending_imports returned no ID")
    print(f"pending_import_id={import_id}")

    # ── 5. Verify RabbitMQ is reachable ───────────────────────────────────────
    print("Checking RabbitMQ management API...")
    mq("GET", "/api/overview")

    # ── 6. Declare queue (idempotent) ─────────────────────────────────────────
    print("Declaring veloci.jobs queue (durable)...")
    mq("PUT", "/api/queues/%2F/veloci.jobs", {"durable": True, "arguments": {}})

    # ── 7. Publish job message ────────────────────────────────────────────────
    job_message = json.dumps({
        "job_id":    job_id,
        "entity_id": entity_id,
        "job_type":  "import.process",
        "metadata":  {"pending_import_id": import_id},
    })
    print(f"Publishing to veloci.jobs...")
    result = mq("POST", "/api/exchanges/%2F/amq.default/publish", {
        "properties":       {"delivery_mode": 2},  # persistent
        "routing_key":      "veloci.jobs",
        "payload":          job_message,
        "payload_encoding": "string",
    })

    if not result.get("routed"):
        print("WARNING: Message published but not routed — the engine consumer may not be running yet.")
        print("         Start the engine and it will pick this up from the queue.")
    else:
        print("Message routed to consumer.")

    print(f"\nDone.")
    print(f"  job_id           = {job_id}")
    print(f"  pending_import_id = {import_id}")
    print(f"  entity_id        = {entity_id}")


if __name__ == "__main__":
    main()
