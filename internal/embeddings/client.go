// Package embeddings provides an OpenAI-compatible embedding client.
package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client sends text to the configured service; callers must obtain user consent
// before sending local knowledge to a third party.
type Client struct {
	Endpoint, Model, APIKey string
	HTTPClient              *http.Client
}

func (c Client) Embed(ctx context.Context, input []string) ([][]float32, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("embedding endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	if strings.TrimSpace(c.Model) == "" || len(input) == 0 || len(input) > 32 {
		return nil, errors.New("invalid embedding model or batch size")
	}
	for _, s := range input {
		if strings.TrimSpace(s) == "" || len(s) > 128*1024 {
			return nil, errors.New("invalid embedding input")
		}
	}
	payload, err := json.Marshal(map[string]any{"model": c.Model, "input": input, "encoding_format": "float"})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c.HTTPClient != nil {
		client.Transport = c.HTTPClient.Transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("embedding service request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding service returned HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := dec.Decode(&decoded); err != nil {
		return nil, errors.New("invalid embedding response")
	}
	if len(decoded.Data) != len(input) {
		return nil, errors.New("embedding response count mismatch")
	}
	result := make([][]float32, len(input))
	dims := 0
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(result) || result[item.Index] != nil || len(item.Embedding) == 0 || len(item.Embedding) > 8192 {
			return nil, errors.New("invalid embedding vector")
		}
		if dims == 0 {
			dims = len(item.Embedding)
		}
		if len(item.Embedding) != dims {
			return nil, errors.New("embedding dimensions mismatch")
		}
		for _, v := range item.Embedding {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, errors.New("invalid embedding value")
			}
		}
		result[item.Index] = item.Embedding
	}
	return result, nil
}
