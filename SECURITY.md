# Security

## Reporting

Report vulnerabilities privately to the repository owner — do not open
public issues for security findings.

## Posture

- Every route requires `Authorization: Bearer $INTERNAL_SERVICE_SECRET`
  except `GET /healthz`. Empty secret fails closed (all 401).
- All third-party page egress goes through go-wowa or first-party API
  endpoints; candidate URLs get an SSRF screen at funnel ingress and an
  adapter-manifest host check at the result boundary.
- Raw email is sensitive: the IMAP poller and CF worker never log message
  bodies; orders store parsed fields only (subject, totals, tracking),
  not raw MIME.
- Order ingest body is capped at 2 MiB.
- jeff prompts carry only allowlisted candidate fields — the injection
  canary (`product_probe`) verifies scraped page content cannot reach the
  LLM verbatim.
- Secrets live in deploy `.env` / systemd EnvironmentFile / Worker
  `secret_text` bindings — never in the repo.
