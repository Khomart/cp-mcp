# Lambda's provided.al2023 runtime runs a Linux binary named "bootstrap".
# The build must finish before Terraform zips it, so always deploy via `make deploy`.

BUILD_DIR := build
# Override if the Claude Code CLI is not on PATH: make mcp-install CLAUDE=/path/to/claude
CLAUDE ?= claude

.PHONY: build mcp mcp-install test deploy plan destroy clean

build:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" \
		-o $(BUILD_DIR)/bootstrap ./cmd/api

# MCP server: runs locally on this machine, so build for the host OS/arch.
mcp:
	go build -trimpath -o $(BUILD_DIR)/cp-mcp ./cmd/mcp

# Registers the server with Claude Code (user scope, so it works in every project).
# The API URL and key are read from Terraform outputs and stored in Claude Code's config.
mcp-install: mcp
	"$(CLAUDE)" mcp add cp --scope user \
		-e CP_API_URL="$$(terraform -chdir=terraform output -raw api_url)" \
		-e CP_API_KEY="$$(terraform -chdir=terraform output -raw api_key)" \
		-- "$(CURDIR)/$(BUILD_DIR)/cp-mcp"

test:
	go test ./...

plan: build
	terraform -chdir=terraform plan

deploy: build
	terraform -chdir=terraform apply

destroy:
	terraform -chdir=terraform destroy

clean:
	rm -rf $(BUILD_DIR) terraform/build
