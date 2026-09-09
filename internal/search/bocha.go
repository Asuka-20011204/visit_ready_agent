package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"visitready/internal/domain"
	"visitready/internal/guard"
)

const maxResponseBytes = 1 << 20

type BochaClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

type bochaRequest struct {
	Query     string `json:"query"`
	Summary   bool   `json:"summary"`
	Freshness string `json:"freshness"`
	Count     int    `json:"count"`
}

type bochaResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		WebPages struct {
			Value []struct {
				Name    string `json:"name"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
				Summary string `json:"summary"`
			} `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

func NewBochaClient(endpoint, apiKey string, client *http.Client) (*BochaClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid bocha endpoint")
	}
	if parsed.Scheme != "https" && !isLoopback(parsed.Hostname()) {
		return nil, errors.New("bocha endpoint must use HTTPS")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("bocha API key is required")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	return &BochaClient{endpoint: endpoint, apiKey: apiKey, httpClient: client}, nil
}

func (c *BochaClient) Search(ctx context.Context, query string, allowedDomains []string) ([]domain.Source, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 120 {
		return nil, errors.New("search query must contain 1 to 120 characters")
	}
	if len(guard.ScanPII(query)) > 0 {
		return nil, errors.New("search query contains direct personal identifiers")
	}
	if len(allowedDomains) == 0 {
		return nil, errors.New("at least one allowed domain is required")
	}

	payload, err := json.Marshal(bochaRequest{
		Query:     query,
		Summary:   true,
		Freshness: "noLimit",
		Count:     5,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Bocha request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Bocha request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search trusted sources: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("search provider returned status %d", resp.StatusCode)
	}

	var decoded bochaResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Bocha response: %w", err)
	}
	if decoded.Code != 0 && decoded.Code != http.StatusOK {
		return nil, fmt.Errorf("search provider returned code %d", decoded.Code)
	}

	results := decoded.Data.WebPages.Value
	sources := make([]domain.Source, 0, len(results))
	for _, result := range results {
		if !guard.IsAllowedSourceURL(result.URL, allowedDomains) {
			continue
		}
		parsed, err := url.Parse(result.URL)
		if err != nil {
			continue
		}
		domainName := guard.MatchAllowedDomain(parsed.Hostname(), allowedDomains)
		snippet := strings.TrimSpace(result.Summary)
		if snippet == "" {
			snippet = strings.TrimSpace(result.Snippet)
		}
		sources = append(sources, domain.Source{
			Title:   truncate(strings.TrimSpace(result.Name), 180),
			URL:     result.URL,
			Domain:  domainName,
			Snippet: truncate(snippet, 1200),
		})
	}
	return sources, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func truncate(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
