# AGENTS.md

Guidance for coding agents working in this repo. See README.md for what the project does and how to use it, and `docs/production-plan.md` for the production roadmap. When you complete a task from that plan, tick its checkbox.

## Layout

- `cmd/api/`: Lambda handler (`handler.go`), wiring (`main.go`), tests (`handler_test.go`). Package `main`.
- `cmd/mcp/main.go`: MCP server entry point over stdio.
- `pkg/cp-mcp/` (package `cpmcp`): MCP tools and prompts (`cp.go`), HTTP client for the files API (`client.go`), tests (`cp_test.go`).
- `terraform/`: all AWS infrastructure. `main.tf` holds every resource.
- One Go module, `cp-mcp`, at the repo root. Uses the official MCP SDK, `github.com/modelcontextprotocol/go-sdk` v1.8.

## Commands

```bash
make test                                  # run after any Go change
go vet ./...
gofmt -l .                                 # must print nothing
make build                                 # Lambda binary (linux/arm64)
make mcp                                   # MCP server binary (host platform)
terraform -chdir=terraform fmt -check      # after any .tf change
terraform -chdir=terraform validate
```

Before finishing a change, run `make test` and `go vet ./...`. If you touched Terraform, also run `fmt -check` and `validate`.

## Do not run without the user's explicit approval

These change shared or persistent state:

- `make deploy`, `terraform apply`, `terraform destroy`, `make destroy`
- `make mcp-install`, `claude mcp add`, `claude mcp remove`
- Anything that writes to the deployed API, S3 or DynamoDB. Read-only smoke tests are fine when the user asks for verification.

`make plan` is safe.

## Secrets

- **Never print the API key.** Read it inline, as in `KEY=$(terraform -chdir=terraform output -raw api_key)`, and never echo it or write it to a file.
- `terraform/terraform.tfstate` and `~/.claude.json` contain the key in plain text. Don't read, print or copy them.
- Don't hardcode the deployed API URL, account ID or key in code or docs. Use `terraform output`.

## Conventions

### Lambda (`cmd/api`)
- **Routing** is a `switch` in `app.handle` on method and path. API Gateway uses a single `$default` route, so new routes need no Terraform changes.
- **AWS clients** sit behind narrow interfaces (`blobStore`, `fileTable`) so tests can use fakes. When you call a new SDK method, add it to the interface and to the fakes in `handler_test.go`.
- **Responses:** use `jsonResponse` and `errorResponse`. Errors are `{"error": "..."}`, and AWS error details are logged with `slog`, not returned to the client.
- **Write order:** S3 first, then DynamoDB. If the DynamoDB write fails, delete the S3 object. Keep this order so the database never points at a missing file.
- **Auth:** every route except `/health` goes through `authorized`, which compares keys in constant time. The key is loaded from SSM once per cold start, in `main.go`.
- **IDs** are canonical lowercase UUIDs. Parse incoming IDs with `uuid.Parse` and use `.String()` as the lookup key.
- **Payloads** are limited to about 6 MB in each direction. Don't design features that assume larger payloads.

### MCP server (`pkg/cp-mcp`, `cmd/mcp`)
- **stdout carries the MCP protocol.** Never write to stdout. Log to stderr only (the `log` package's default).
- **Tools** are registered with `mcp.AddTool` and typed input/output structs. Field descriptions go in the `jsonschema:"..."` tag. Return errors from handlers; the SDK turns them into `IsError` tool results.
- **Prompts** are registered with `server.AddPrompt`. In Claude Code, prompts appear as `/mcp__cp__<name>` and tools as `mcp__cp__<name>`.
- The server reaches storage only through the `store` interface, which `Client` implements. Tests use `fakeStore` over `mcp.NewInMemoryTransports()`. Test the client with `httptest`, never against the live API.
- **The server can't read the conversation.** `copy` relies on the model to send the text. Don't add features that assume access to the chat.

### Terraform
- **Version:** requires Terraform 1.10 or later (`required_version` in `versions.tf`); the user runs 1.16. Use `use_lockfile = true` for S3 state locking, not a DynamoDB lock table.
- **IAM is least-privilege**, scoped to this project's table, bucket and parameter. Add only the actions code actually uses.
- **Resource names** follow `${var.project_name}-${var.environment}-…` via `local.name`.
- **The Lambda zip comes from `build/bootstrap`,** which must exist before `plan` or `apply`. Use the `make` targets, which build first.
- **Region:** the stack is in `us-east-1`, but the user's AWS CLI defaults to `us-east-2`. Pass `--region us-east-1` to `aws` commands.
- **Avoid changing the DynamoDB key schema or any resource names.** Either change replaces the resource and destroys its data.

## Gotchas

- **Lambda architecture:** `GOARCH=arm64` in the Makefile must match `architectures = ["arm64"]` in Terraform. A mismatch gives "exec format error" at runtime.
- **New dependencies:** run `go mod tidy` after adding imports.
- **Paths from `api_url`:** the Terraform output ends with `/`. Build URLs as `${API_URL}files`, not `${API_URL}/files`. `Client` strips the trailing slash itself.
- **Header names:** HTTP API events deliver header names in lowercase. Read `req.Headers["x-api-key"]`.
- **Not a git repo:** the project isn't under version control yet, so there's no history to recover from. Read files before overwriting them.
