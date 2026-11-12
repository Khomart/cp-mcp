// Package cpmcp is an MCP server that copies conversations to the files API
// and pastes them back into another conversation.
//
// MCP servers can't read the chat themselves, so the two directions work differently:
//   - copy: the "copy" prompt tells the model to call the "copy" tool with the
//     conversation text. The model writes that text, so it's a faithful
//     reconstruction rather than a byte-exact transcript.
//   - paste: the "paste" prompt fetches the stored text server-side and injects it
//     into the chat directly, so what you paste is exactly what was stored.
package cpmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// store is the subset of Client the server needs; tests swap in a fake.
type store interface {
	Store(ctx context.Context, text string) (string, error)
	Fetch(ctx context.Context, id string) (string, error)
}

type CopyInput struct {
	Content string `json:"content" jsonschema:"The full conversation text to save"`
}

type CopyOutput struct {
	ID string `json:"id" jsonschema:"GUID to pass to paste later"`
}

type PasteInput struct {
	ID string `json:"id" jsonschema:"GUID returned by an earlier copy"`
}

type PasteOutput struct {
	Content string `json:"content" jsonschema:"The stored conversation text"`
}

const copyInstructions = `Save this conversation using the "copy" tool from the cp MCP server.

Pass the conversation so far as "content":
- Include every user and assistant message, in order, from the start of the conversation up to (not including) this request.
- Prefix each message with "User:" or "Assistant:" and separate messages with a blank line.
- Reproduce messages verbatim, including code blocks. Do not summarize, shorten, or omit anything.
- Where you used tools, include a one-line note of the tool and what it did, e.g. "[tool: ran tests — all passed]".

After the tool returns, reply with just the id it returned.`

func NewServer(s store) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "cp", Version: "0.1.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "copy",
		Description: "Save text (typically the current conversation) to cloud storage. Returns an id that paste can retrieve later.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CopyInput) (*mcp.CallToolResult, CopyOutput, error) {
		if strings.TrimSpace(in.Content) == "" {
			return nil, CopyOutput{}, errors.New("content is empty")
		}
		id, err := s.Store(ctx, in.Content)
		if err != nil {
			return nil, CopyOutput{}, fmt.Errorf("copy failed: %w", err)
		}
		return nil, CopyOutput{ID: id}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "paste",
		Description: "Retrieve text previously saved with copy, by its id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PasteInput) (*mcp.CallToolResult, PasteOutput, error) {
		content, err := s.Fetch(ctx, strings.TrimSpace(in.ID))
		if err != nil {
			return nil, PasteOutput{}, fmt.Errorf("paste failed: %w", err)
		}
		return nil, PasteOutput{Content: content}, nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "copy",
		Description: "Copy this conversation to cloud storage and get back an id",
	}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: copyInstructions}}},
		}, nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "paste",
		Description: "Paste a previously copied conversation into this one",
		Arguments: []*mcp.PromptArgument{{
			Name:        "id",
			Description: "The id returned by copy",
			Required:    true,
		}},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := strings.TrimSpace(req.Params.Arguments["id"])
		if id == "" {
			return nil, errors.New("usage: paste <id>")
		}
		content, err := s.Fetch(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("paste failed: %w", err)
		}
		text := fmt.Sprintf(`Below is a conversation copied from another session (id %s). Treat it as background context for this conversation. Briefly acknowledge what it covers, then wait for my next request.

<copied_conversation>
%s
</copied_conversation>`, id, content)
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})

	return server
}
