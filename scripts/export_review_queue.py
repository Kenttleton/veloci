#!/usr/bin/env python3
"""Export stage 2 review_queue results to CSV files.

Produces two files:
  - review_queue_clusters.csv   — one row per cluster (summary)
  - review_queue_transactions.csv — one row per transaction (detail)
"""
import csv
import os
import subprocess
import sys

# ── Config ─────────────────────────────────────────────────────────────────────
pg_host = os.environ.get("VELOCI_DB_HOST", "localhost")
pg_port = os.environ.get("VELOCI_DB_PORT", "5432")
pg_user = os.environ.get("VELOCI_APP_DB_USER", os.environ.get("VELOCI_DB_USER", "veloci_app"))
pg_db   = os.environ.get("VELOCI_APP_DB",      os.environ.get("VELOCI_DB_NAME", "veloci_app"))
pg_pass = os.environ.get("VELOCI_APP_DB_PASSWORD", os.environ.get("VELOCI_DB_PASSWORD", ""))

out_dir = os.path.join(os.path.dirname(__file__), "..", "data")
os.makedirs(out_dir, exist_ok=True)

clusters_out     = os.path.join(out_dir, "review_queue_clusters.csv")
transactions_out = os.path.join(out_dir, "review_queue_transactions.csv")

# ── psql helper ────────────────────────────────────────────────────────────────
def psql(sql: str) -> str:
    env = {**os.environ, "PGPASSWORD": pg_pass}
    result = subprocess.run(
        ["psql", "-h", pg_host, "-p", pg_port, "-U", pg_user, "-d", pg_db,
         "-t", "-A", "-q", "-F", "\t", "-c", sql],
        capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        print(f"psql error: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    return result.stdout.strip()


# ── Clusters ───────────────────────────────────────────────────────────────────
CLUSTERS_SQL = """
SELECT
  rq.id::text                                            AS cluster_id,
  rq.suggested_name                                      AS name,
  rq.suggested_entry_type                                AS entry_type,
  r.direction                                            AS direction,
  round(rq.confidence::numeric, 4)                       AS confidence,
  rq.matched_transaction_count                           AS tx_count,
  round((rq.suggested_rate_per_day * 30)::numeric, 2)   AS est_monthly_cents,
  round(rq.suggested_rate_per_day::numeric, 4)           AS rate_per_day,
  r.created_at::date                                     AS detected_on
FROM review_queue rq
JOIN rules r ON r.id = rq.rule_id
ORDER BY rq.matched_transaction_count DESC, rq.suggested_name
"""

CLUSTERS_HEADER = [
    "cluster_id", "name", "entry_type", "direction",
    "confidence", "tx_count", "est_monthly_cents", "rate_per_day", "detected_on",
]

# ── Transactions ───────────────────────────────────────────────────────────────
TRANSACTIONS_SQL = """
SELECT
  rq.id::text                        AS cluster_id,
  rq.suggested_name                  AS cluster_name,
  rq.suggested_entry_type            AS entry_type,
  rt.date::text                      AS date,
  rt.amount_cents                    AS amount_cents,
  rt.imported_payee                  AS payee,
  rt.merchant_normalized             AS merchant_normalized,
  round(tra.confidence::numeric, 4)  AS assignment_confidence
FROM review_queue rq
JOIN transaction_rule_assignments tra ON tra.rule_id = rq.rule_id
JOIN raw_transactions rt ON rt.id = tra.transaction_id
ORDER BY rq.suggested_name, rt.date
"""

TRANSACTIONS_HEADER = [
    "cluster_id", "cluster_name", "entry_type",
    "date", "amount_cents", "payee", "merchant_normalized", "assignment_confidence",
]

# ── Export ─────────────────────────────────────────────────────────────────────
def export(sql: str, header: list, path: str, label: str) -> int:
    rows_raw = psql(sql)
    if not rows_raw:
        print(f"  no rows returned for {label}")
        return 0
    rows = [line.split("\t") for line in rows_raw.splitlines()]
    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(header)
        w.writerows(rows)
    print(f"  {label}: {len(rows)} rows → {path}")
    return len(rows)


def main():
    print("Exporting review_queue...")
    n_clusters = export(CLUSTERS_SQL, CLUSTERS_HEADER, clusters_out, "clusters")
    n_tx       = export(TRANSACTIONS_SQL, TRANSACTIONS_HEADER, transactions_out, "transactions")
    print(f"Done. {n_clusters} clusters, {n_tx} transaction rows.")


if __name__ == "__main__":
    main()
