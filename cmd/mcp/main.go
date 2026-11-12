package main

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	cpmcp "cp-mcp/pkg/cp-mcp"
)

func main() {
	// stdout carries the MCP protocol, so diagnostics must go to stderr (log's default).
	apiURL, apiKey := os.Getenv("CP_API_URL"), os.Getenv("CP_API_KEY")
	if apiURL == "" || apiKey == "" {
		log.Fatal("CP_API_URL and CP_API_KEY must be set (see `terraform -chdir=terraform output`)")
	}

	server := cpmcp.NewServer(cpmcp.NewClient(apiURL, apiKey))
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
