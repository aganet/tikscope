// Package routeros polls the RouterOS REST API for throughput, health and
// device identities.
package routeros

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base     string
	user     string
	password string
	http     *http.Client
}

func NewClient(baseURL, user, password string, insecure bool) *Client {
	return &Client{
		base:     strings.TrimRight(baseURL, "/"),
		user:     user,
		password: password,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}},
		},
	}
}

// NotFound is returned for 404s so optional features can be skipped quietly.
type NotFound struct{ Path string }

func (e NotFound) Error() string { return "routeros: " + e.Path + " not found" }

// Get decodes GET /rest<path> into out. RouterOS returns every scalar as a
// string, so out fields are strings parsed by the caller.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/rest"+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return NotFound{Path: path}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// atoi reads the numeric strings RouterOS returns, ignoring a trailing unit.
func atoi(s string) int64 {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.ParseInt(s[:end], 10, 64)
	if neg {
		n = -n
	}
	return n
}

// isTrue reads RouterOS booleans ("true"/"false").
func isTrue(s string) bool { return s == "true" || s == "yes" }

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.' || s[end] == '-') {
		end++
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	return v, err == nil
}

// Post runs a RouterOS command (print, monitor) and decodes its reply.
func (c *Client) Post(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/rest"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return NotFound{Path: path}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
