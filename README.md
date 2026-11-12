# cp-mcp

Copy a Claude conversation to the cloud and paste it into another session.

The project has two parts:

- **Files API**: a Go Lambda behind API Gateway that stores text in S3 and indexes it in DynamoDB by GUID, protected by an API key.
- **`cp` MCP server**: a local MCP server that adds `copy` and `paste` to Claude Code (or any MCP client) and talks to the files API.

```
Claude Code ──stdio──▶ cp-mcp (local) ──HTTPS + x-api-key──▶ API Gateway ──▶ Lambda ──▶ S3 (text)
                                                                                  └──▶ DynamoDB (id → file_name)
```

## Requirements

- Go 1.26+
- Terraform 1.10+
- AWS credentials with permission to create the resources below (`aws configure` or `AWS_PROFILE`)
- Claude Code CLI, to register the MCP server

## Quick start

```bash
make deploy        # build the Lambda and run terraform apply
make mcp-install   # build the MCP server and register it with Claude Code
```

Then start a **new** Claude Code session. Sessions that were already running don't pick up the server.

If `claude` isn't on your PATH (for example, you only have the desktop app), pass the CLI's path:

```bash
make mcp-install CLAUDE=/path/to/claude
```

## Using copy and paste

| Action | How |
|---|---|
| Save the current conversation | `/mcp__cp__copy`, or ask "save this conversation" |
| Load a saved conversation | `/mcp__cp__paste <guid>`, or ask "load conversation `<guid>`" |

- **Copy:** MCP servers can't read the chat, so the model writes the conversation out and passes it to the `copy` tool. The saved text is the model's faithful reconstruction, not a byte-exact transcript.
- **Paste with the slash command:** the server fetches the stored text and inserts it into the chat exactly as stored.
- **Paste with a plain request:** the model calls the `paste` tool and reads the text as a tool result.

To skip the approval prompt each time a tool runs, add this to `~/.claude/settings.json`:

```json
{ "permissions": { "allow": ["mcp__cp__copy", "mcp__cp__paste"] } }
```

## Files API

The base URL comes from `terraform -chdir=terraform output -raw api_url`. Every route except `/health` requires the `x-api-key` header.

| Method | Path | Body | Response |
|---|---|---|---|
| `GET` | `/health` | none | `200 {"ok":true}` |
| `POST` | `/files` | raw text | `201 {"id":"<guid>"}` |
| `GET` | `/files/{id}` | none | `200` the text (`text/plain`) |

Errors are returned as `{"error":"..."}`:
- `400`: the body is empty or the ID isn't a GUID
- `401`: the key is missing or wrong
- `404`: the file doesn't exist
- `500`: storage failed

Example:

```bash
export API_URL=$(terraform -chdir=terraform output -raw api_url)
export API_KEY=$(terraform -chdir=terraform output -raw api_key)
```
```bash
curl -X POST "${API_URL}files" -H "x-api-key: $API_KEY" --data-binary @notes.txt
```
```bash
curl "${API_URL}files/<guid>" -H "x-api-key: $API_KEY"
```

Each upload creates `files/<guid>.txt` in S3 and a DynamoDB item with these fields: `id`, `file_name`, `size_bytes`, `created_at`.

## Infrastructure

Everything is defined in `terraform/` and deployed to `us-east-1` by default.

| Resource | Name |
|---|---|
| Lambda (Go, `provided.al2023`, arm64) | `cp-mcp-dev-api` |
| API Gateway HTTP API (50 req/s, burst 100) | `cp-mcp-dev-http-api` |
| S3 bucket (private, encrypted, versioned) | `cp-mcp-dev-data-<account-id>` |
| DynamoDB table (on-demand, PITR) | `cp-mcp-dev-files` |
| API key (SSM SecureString) | `/cp-mcp/dev/api-key` |

The settings you can change are in `terraform/variables.tf`: region, environment, memory, timeout, CORS, throttling and log retention.

**To rotate the API key**, run the command below, then `make mcp-install` again (remove the old registration first with `claude mcp remove cp -s user`):

```bash
terraform -chdir=terraform apply -replace=random_password.api_key
```

## Development

```bash
make test      # go test ./...
make build     # Lambda binary → build/bootstrap (linux/arm64)
make mcp       # MCP server → build/cp-mcp (host platform)
make plan      # build + terraform plan
make clean
```

| Path | Contents |
|---|---|
| `cmd/api/` | Lambda: routing, API key check, S3/DynamoDB logic, tests |
| `cmd/mcp/` | MCP server entry point (reads `CP_API_URL`, `CP_API_KEY`) |
| `pkg/cp-mcp/` | MCP tools and prompts, HTTP client for the files API, tests |
| `terraform/` | All AWS infrastructure |

After changing the MCP server, run `make mcp`. New sessions use the new binary.

## Limitations

- **About 6 MB per request and response:** Lambda's limit on request and response size. Larger payloads would need presigned S3 uploads.
- **Copies can be incomplete:** the model writes the conversation out, so very long chats may be shortened.
- **Large pastes fill context:** pasted text is added to the chat in full and takes up the new session's context window.
- **No delete endpoint:** remove stored files in the AWS console.
- **Local Terraform state:** state, including the API key, is stored locally in `terraform/terraform.tfstate`. Turn on the S3 backend in `terraform/versions.tf` before sharing the project or deploying from CI.
- **API key stored in plain text:** the key is saved in `~/.claude.json` as part of the MCP server's registration.
