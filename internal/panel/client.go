package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (c *Client) Config(ctx context.Context, nodeID, nodeType, etag string) (*NodeConfig, string, bool, error) {
	var result NodeConfig
	next, changed, err := c.get(ctx, nodeID, nodeType, "/api/v2/server/config", etag, &result)
	if err != nil || !changed {
		return nil, next, changed, err
	}
	return &result, next, true, nil
}

func (c *Client) Users(ctx context.Context, nodeID, nodeType, etag string) (*UsersResponse, string, bool, error) {
	var result UsersResponse
	next, changed, err := c.get(ctx, nodeID, nodeType, "/api/v2/server/user", etag, &result)
	if err != nil || !changed {
		return nil, next, changed, err
	}
	return &result, next, true, nil
}

func (c *Client) Report(ctx context.Context, nodeID, nodeType string, report Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := c.request(ctx, nodeID, nodeType, http.MethodPost, "/api/v2/server/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp)
	}
	return nil
}

func (c *Client) get(ctx context.Context, nodeID, nodeType, path, etag string, target any) (string, bool, error) {
	req, err := c.request(ctx, nodeID, nodeType, http.MethodGet, path, nil)
	if err != nil {
		return etag, false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return etag, false, err
	}
	defer resp.Body.Close()
	next := resp.Header.Get("ETag")
	if next == "" {
		next = etag
	}
	if resp.StatusCode == http.StatusNotModified {
		return next, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return etag, false, responseError(resp)
	}
	if err = json.NewDecoder(resp.Body).Decode(target); err != nil {
		return etag, false, fmt.Errorf("decode %s response: %w", path, err)
	}
	return next, true, nil
}

func (c *Client) request(ctx context.Context, nodeID, nodeType, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("token", c.token)
	req.Header.Set("node-id", nodeID)
	if nodeType != "" {
		req.Header.Set("node-type", nodeType)
	}
	return req, nil
}

func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("panel %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
