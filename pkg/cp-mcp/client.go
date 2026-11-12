package cpmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the files API deployed by terraform/ (API Gateway + Lambda).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Store saves text and returns the GUID the API assigned to it.
func (c *Client) Store(ctx context.Context, text string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files", strings.NewReader(text))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "text/plain; charset=utf-8")

	body, err := c.do(req, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("unexpected response from API: %s", body)
	}
	return out.ID, nil
}

// Fetch returns the text stored under id.
func (c *Client) Fetch(ctx context.Context, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/files/"+url.PathEscape(id), nil)
	if err != nil {
		return "", err
	}
	body, err := c.do(req, http.StatusOK)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *Client) do(req *http.Request, wantStatus int) ([]byte, error) {
	req.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading API response: %w", err)
	}
	if resp.StatusCode != wantStatus {
		// The API returns {"error": "..."}; fall back to the raw body otherwise.
		var apiErr struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			msg = apiErr.Error
		}
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}
