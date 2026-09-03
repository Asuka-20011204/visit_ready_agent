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

type TavilyClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

type tavilyRequest struct {
	APIKey            string   `json:"api_key"`
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth"`
	IncludeAnswer     bool     `json:"include_answer"`
	IncludeRawContent bool     `json:"include_raw_content"`
	MaxResults        int      `json:"max_results"`
	IncludeDomains    []string `json:"include_domains"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func NewTavilyClient(endpoint, apiKey string, client *http.Client) (*TavilyClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid Tavily endpoint")
	}
	if parsed.Scheme != "https" && !isLoopback(parsed.Hostname()) {
		return nil, errors.New("Tavily endpoint must use HTTPS")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("Tavily API key is required")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	return &TavilyClient{endpoint: endpoint, apiKey: apiKey, httpClient: client}, nil
}

func (c *TavilyClient) Search(ctx context.Context, query string, allowedDomains []string) ([]domain.Source, error) {
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

	payload, err := json.Marshal(tavilyRequest{
		APIKey:            c.apiKey,
		Query:             query,
		SearchDepth:       "advanced",
		IncludeAnswer:     false,
		IncludeRawContent: false,
		MaxResults:        5,
		IncludeDomains:    append([]string(nil), allowedDomains...),
	})
	if err != nil {
		return nil, fmt.Errorf("encode Tavily request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Tavily request: %w", err)
	}
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

	var decoded tavilyResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Tavily response: %w", err)
	}

	sources := make([]domain.Source, 0, len(decoded.Results))
	for _, result := range decoded.Results {
		if !guard.IsAllowedSourceURL(result.URL, allowedDomains) {
			continue
		}
		parsed, err := url.Parse(result.URL)
		if err != nil {
			continue
		}
		domainName := guard.MatchAllowedDomain(parsed.Hostname(), allowedDomains)
		sources = append(sources, domain.Source{
			Title:   truncate(strings.TrimSpace(result.Title), 180),
			URL:     result.URL,
			Domain:  domainName,
			Snippet: truncate(strings.TrimSpace(result.Content), 1200),
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
