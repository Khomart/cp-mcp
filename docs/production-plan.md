# Production readiness plan

**Status:** draft · **Last updated:** 2026-11-11 · **Baseline:** commit `a41aff5` (POC)

This plan takes cp-mcp from a single-user POC to a service other people can rely on. It's split into four phases. Each phase ships on its own and leaves the system working.

## Where we are

| Area | Current state (POC) | Gap |
|---|---|---|
| Auth | One shared API key in SSM, checked by the Lambda | No per-user identity. Any key holder can read every file |
| Key on clients | Stored in plain text in `~/.claude.json` | Easy to leak, and rotating it is manual |
| Data protection | S3 SSE-S3 (AES256), DynamoDB default encryption, PITR | No customer-managed key, no retention, no delete |
| Payload size | About 6 MB per request and per response | Long conversations fail |
| Terraform | Local state, one `dev` environment | State is on one laptop, there's no prod, `destroy` can wipe data |
| Delivery | `make deploy` from a laptop | No CI, no review gate, no repeatable deploys |
| Observability | Logs only (14-day retention) | No alarms, tracing or cost alerts |
| MCP server | Local binary built with `make mcp` | Manual install, Claude Code only, plain-text key |
| Copy fidelity | The model reconstructs the conversation | Not exact; long chats get shortened |

## Decisions needed before starting

These change the scope of Phases 2 and 4. Decide them before Phase 2 starts.

1. **Audience: just me, a team, or external users?**
   - Just me: Phase 2 can be scaled down to rotating the key plus keeping it in the keychain.
   - A team: use the company's identity provider through a JWT authorizer.
   - External users: use Cognito user pools, and plan for abuse handling and quotas.
2. **Identity provider:** Cognito, Auth0/Okta, or the company's existing one.
3. **Retention:** how long stored conversations are kept by default. Suggested: 90 days, with a per-file override.
4. **MCP delivery:** keep the local binary, or move to a remote MCP server with OAuth (Phase 4). The remote option removes keys from clients completely.
5. **AWS account layout:** dev and prod in one account, or separate accounts. Separate accounts are recommended.

## Phase 1: Foundations (about 2–3 days)

Goal: safe, repeatable deploys. Everything later depends on this.

- [ ] **Remote state.** Add a small bootstrap config (`terraform/bootstrap/`) that creates the state bucket: versioned, encrypted, public access blocked. Turn on the `s3` backend in `versions.tf` with `use_lockfile = true`, then run `terraform init -migrate-state`.
- [ ] **Environments.** Use a separate state file per environment, plus `envs/dev.tfvars` and `envs/prod.tfvars`. The existing `dev` resource names must not change, so nothing gets replaced.
- [ ] **Deletion safety.** Set `deletion_protection_enabled = true` on the DynamoDB table, and add `lifecycle { prevent_destroy = true }` to the table and the data bucket.
- [ ] **CI/CD with GitHub Actions.**
  - On every pull request: `go vet`, `gofmt` check, `go test`, `terraform fmt -check`, `validate`, and `plan` (posted as a PR comment).
  - On merge to `main`: `apply` to dev automatically. Prod applies require a manual approval.
  - AWS access goes through an **OIDC role**, never stored keys. The role and OIDC provider are defined in Terraform.
- [ ] **Static analysis.** Add `golangci-lint` and `govulncheck` to CI.
- [ ] **Cost guardrail.** Add an AWS Budget alert, at a monthly threshold to be agreed.

**Done when:** state is in S3; `plan` for dev shows no replacements; a pull request runs every check; merging deploys to dev; and running `terraform destroy` on prod fails because of the protection settings.

## Phase 2: Security and data protection (about 3–5 days)

Goal: each user can only reach their own data, and data has a defined lifecycle.

- [ ] **Per-user auth.** Add a JWT authorizer on the HTTP API, using the identity provider from decision 2, and remove the shared-key check.
  - Fallback if the audience is "just me": keep the key, but add a rotation runbook and keychain storage.
- [ ] **Ownership.** Store `owner` (the token's `sub`) on each item. `GET` and `DELETE` return 404 when the caller isn't the owner, so the API doesn't reveal that the file exists.
- [ ] **`DELETE /files/{id}`.** Removes the DynamoDB item and **all S3 versions** of the object.
- [ ] **Customer-managed KMS key (CMK).** Encrypt S3 (SSE-KMS with a bucket key) and DynamoDB with it. Only the Lambda role and admins get permission to use it.
- [ ] **Retention.**
  - Set a DynamoDB TTL on `expires_at`, using the retention period from decision 3.
  - Add an S3 lifecycle rule that expires current objects after the retention period plus a grace period, and old versions after 7 days.
- [ ] **Edge hardening.** Restrict CORS to specific origins. Add AWS WAF with AWS's managed rule sets and a per-IP rate limit.
- [ ] **Audit.** Turn on CloudTrail data events for the bucket. Log the owner and file ID (never file content) for every access.
- [ ] **No plain-text keys on clients.** The MCP server reads its credentials from the OS keychain. Phase 4 removes client keys entirely.

**Done when:**
- A user can't read another user's file, which an integration test proves.
- Deleted files can't be recovered from S3.
- Data encrypted with the CMK can't be read without permission on that key.
- Expired files disappear on their own.

## Phase 3: Scale and operations (about 3–4 days)

Goal: handle real conversation sizes, and know when something breaks.

- [ ] **Presigned transfers.**
  - `POST /v1/files` creates the record and returns `{id, upload_url}`. The client uploads straight to S3.
  - `GET /v1/files/{id}` returns `{download_url, metadata}`.
  - Set an explicit maximum size, enforced through presigned-POST conditions.
- [ ] **Two-phase records.** New items are written as `status = pending`. An S3 `ObjectCreated` event marks them `ready`, and a scheduled job removes pending items that never got an upload. This replaces today's best-effort cleanup.
- [ ] **API versioning and endpoints.** Add a `/v1` prefix and `GET /v1/files` to list the caller's files (paginated). Add metadata: `title`, `source` (project/session), `size_bytes`, `created_at`, `expires_at`.
- [ ] **Observability.**
  - Alarms on Lambda errors and throttles, API 5xx rate, p95 latency, and DynamoDB throttles, sent to SNS and then email or Slack.
  - X-Ray tracing, structured logs with the request ID, and a CloudWatch dashboard.
- [ ] **Contract.** Publish an OpenAPI spec for `/v1`, checked in CI against handler tests.
- [ ] **Custom domain.** Add `api.<domain>` with an ACM certificate and Route 53 records.
- [ ] **Integration tests.** Run a test suite against the dev stack in CI after each deploy.

**Done when:**
- A 50 MB conversation round-trips successfully.
- Every alarm has been triggered once on purpose and reached the channel.
- Failed uploads leave nothing behind after 24 hours.
- Dev CI runs integration tests on every merge.

## Phase 4: MCP productization (about 1–2 weeks)

Goal: install with zero setup, and copies are exact where possible.

- [ ] **Remote MCP server.** Serve the MCP server over the Streamable HTTP transport behind API Gateway, with MCP's standard **OAuth** flow against the identity provider from decision 2. Users add one URL. It works in Claude Code, Claude Desktop and claude.ai, with no binary to install and no key on the client.
- [ ] **Exact copies in Claude Code.** Add an optional hook (`PreCompact` / `SessionEnd`, or one you trigger yourself) that uploads the real transcript (`transcript_path`), tagged with `source = transcript`.
- [ ] **Safe paste.** Check the size before inserting. Above a set limit, paste a summary plus the ID, and let the model read the rest through the `paste` tool.
- [ ] **Local binary, if kept.** Publish signed releases (goreleaser), a Homebrew tap, a version check and a changelog.
- [ ] **Docs.** User guide, privacy notice (what's stored, where, for how long) and an admin runbook.

**Done when:**
- A new user can connect by URL and OAuth alone, in under 2 minutes.
- Transcript copies are byte-exact.
- Pasting a large conversation doesn't exhaust the context window.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| A state migration or resource rename replaces live resources | Data loss | Phase 1 `prevent_destroy`; review every `plan` in the PR; take an on-demand DynamoDB backup before migrations |
| Conversations contain secrets or personal data | Privacy incident | Phase 2 KMS, ownership checks, retention and audit logging; privacy notice |
| A leaked shared key before Phase 2 | Full read access to all stored data | Rotate now if it has been shared; ship Phase 2 before onboarding anyone else |
| The remote MCP and OAuth work is bigger than expected | Phase 4 slips | Keep the local binary working with keychain storage as a fallback |
| Copies of long chats are lossy | Users don't trust copy | Use the transcript hook in Claude Code; label reconstructed copies clearly |

## Out of scope for now

- Running in multiple regions or active-active failover
- Sharing files between users and access-control lists (revisit after Phase 2)
- Full-text search across stored conversations
