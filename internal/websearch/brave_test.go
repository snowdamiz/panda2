package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBraveSearchSendsExpectedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web/search" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "test-key" {
			t.Fatalf("unexpected subscription token %q", got)
		}
		query := r.URL.Query()
		for key, want := range map[string]string{
			"q":              "latest sqlite release",
			"count":          "3",
			"offset":         "1",
			"country":        "US",
			"search_lang":    "en",
			"ui_lang":        "en-US",
			"safesearch":     "strict",
			"freshness":      "pw",
			"result_filter":  "web",
			"extra_snippets": "true",
		} {
			if got := query.Get(key); got != want {
				t.Fatalf("expected %s=%q, got %q", key, want, got)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"query": map[string]any{
				"original":               "latest sqlite release",
				"more_results_available": true,
			},
			"web": map[string]any{
				"results": []map[string]any{{
					"title":          "SQLite Release History",
					"url":            "https://sqlite.org/changes.html",
					"description":    "Recent SQLite releases.",
					"age":            "2 days ago",
					"page_age":       "2026-06-18T00:00:00Z",
					"language":       "en",
					"extra_snippets": []string{"SQLite release notes", "Version details"},
					"profile":        map[string]string{"name": "sqlite.org", "long_name": "SQLite"},
				}},
			},
		})
	}))
	defer server.Close()

	client := NewBraveClient(Config{APIKey: "test-key", BaseURL: server.URL})
	response, err := client.Search(context.Background(), Request{
		Query:         "latest sqlite release",
		Count:         3,
		Offset:        1,
		SafeSearch:    "strict",
		Freshness:     "pw",
		ExtraSnippets: true,
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.Provider != "brave_search" || !response.MoreResultsAvailable {
		t.Fatalf("unexpected response metadata: %+v", response)
	}
	if len(response.Results) != 1 || response.Results[0].Source != "SQLite" || len(response.Results[0].ExtraSnippets) != 2 {
		t.Fatalf("unexpected results: %+v", response.Results)
	}
}

func TestBraveSearchWithoutKey(t *testing.T) {
	client := NewBraveClient(Config{BaseURL: "http://127.0.0.1"})
	_, err := client.Search(context.Background(), Request{Query: "anything"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestBraveSearchParsesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "RATE_LIMITED", "detail": "slow down"},
		})
	}))
	defer server.Close()

	client := NewBraveClient(Config{APIKey: "test-key", BaseURL: server.URL})
	_, err := client.Search(context.Background(), Request{Query: "anything"})
	var braveErr Error
	if !errors.As(err, &braveErr) {
		t.Fatalf("expected Brave error, got %T %v", err, err)
	}
	if braveErr.StatusCode != http.StatusTooManyRequests || !strings.Contains(braveErr.Error(), "RATE_LIMITED") {
		t.Fatalf("unexpected parsed error: %+v", braveErr)
	}
}
