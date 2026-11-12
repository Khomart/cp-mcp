package cpmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeStore struct {
	data map[string]string
	next string
	err  error
}

func (f *fakeStore) Store(_ context.Context, text string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.data[f.next] = text
	return f.next, nil
}

func (f *fakeStore) Fetch(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	text, ok := f.data[id]
	if !ok {
		return "", errors.New("API returned 404: file not found")
	}
	return text, nil
}

// connect runs the server over an in-memory transport and returns a client session to it.
func connect(t *testing.T, s store) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := NewServer(s).Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool returned no content")
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestListsToolsAndPrompts(t *testing.T) {
	cs := connect(t, &fakeStore{data: map[string]string{}})
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var toolNames []string
	for _, tl := range tools.Tools {
		toolNames = append(toolNames, tl.Name)
	}
	if strings.Join(toolNames, ",") != "copy,paste" {
		t.Errorf("tools = %v", toolNames)
	}

	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var promptNames []string
	for _, p := range prompts.Prompts {
		promptNames = append(promptNames, p.Name)
	}
	if strings.Join(promptNames, ",") != "copy,paste" {
		t.Errorf("prompts = %v", promptNames)
	}
}

func TestCopyThenPasteTools(t *testing.T) {
	fs := &fakeStore{data: map[string]string{}, next: "abc-123"}
	cs := connect(t, fs)
	ctx := context.Background()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "copy", Arguments: map[string]any{"content": "User: hi\n\nAssistant: hello"}})
	if err != nil || res.IsError {
		t.Fatalf("copy: err=%v result=%+v", err, res)
	}
	var out CopyOutput
	if err := json.Unmarshal([]byte(toolText(t, res)), &out); err != nil || out.ID != "abc-123" {
		t.Fatalf("copy output = %q", toolText(t, res))
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "paste", Arguments: map[string]any{"id": "abc-123"}})
	if err != nil || res.IsError {
		t.Fatalf("paste: err=%v result=%+v", err, res)
	}
	var pasted PasteOutput
	if err := json.Unmarshal([]byte(toolText(t, res)), &pasted); err != nil || pasted.Content != "User: hi\n\nAssistant: hello" {
		t.Fatalf("paste output = %q", toolText(t, res))
	}
}

func TestCopyToolRejectsEmptyContent(t *testing.T) {
	fs := &fakeStore{data: map[string]string{}, next: "x"}
	cs := connect(t, fs)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "copy", Arguments: map[string]any{"content": "  "}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected tool error for empty content")
	}
	if len(fs.data) != 0 {
		t.Error("nothing should be stored")
	}
}

func TestToolSurfacesAPIErrors(t *testing.T) {
	cs := connect(t, &fakeStore{data: map[string]string{}, err: errors.New("API returned 401: missing or invalid API key")})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "copy", Arguments: map[string]any{"content": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(toolText(t, res), "401") {
		t.Errorf("result = %+v", res)
	}
}

func TestCopyPromptInstructsModel(t *testing.T) {
	cs := connect(t, &fakeStore{data: map[string]string{}})

	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(text, `"copy" tool`) {
		t.Errorf("prompt text = %q", text)
	}
}

func TestPastePromptInjectsStoredText(t *testing.T) {
	fs := &fakeStore{data: map[string]string{"abc-123": "User: earlier chat"}}
	cs := connect(t, fs)

	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "paste", Arguments: map[string]string{"id": " abc-123 "}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(text, "<copied_conversation>\nUser: earlier chat\n</copied_conversation>") {
		t.Errorf("prompt text = %q", text)
	}
}

func TestPastePromptUnknownID(t *testing.T) {
	cs := connect(t, &fakeStore{data: map[string]string{}})

	_, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "paste", Arguments: map[string]string{"id": "nope"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestClientAgainstHTTPServer(t *testing.T) {
	stored := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"missing or invalid API key"}`)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			b, _ := io.ReadAll(r.Body)
			stored["id-1"] = string(b)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"id-1"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/files/"):
			text, ok := stored[strings.TrimPrefix(r.URL.Path, "/files/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":"file not found"}`)
				return
			}
			io.WriteString(w, text)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	// Trailing slash matches the format of `terraform output api_url`.
	c := NewClient(srv.URL+"/", "k")
	id, err := c.Store(ctx, "héllo ✓")
	if err != nil || id != "id-1" {
		t.Fatalf("Store = %q, %v", id, err)
	}
	text, err := c.Fetch(ctx, id)
	if err != nil || text != "héllo ✓" {
		t.Fatalf("Fetch = %q, %v", text, err)
	}
	if _, err := c.Fetch(ctx, "missing"); err == nil || err.Error() != "API returned 404: file not found" {
		t.Errorf("missing err = %v", err)
	}
	if _, err := NewClient(srv.URL, "wrong").Store(ctx, "x"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad key err = %v", err)
	}
}
