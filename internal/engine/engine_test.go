package engine

import (
	"encoding/json"
	"testing"

	"github.com/soulteary/gorge-search/internal/config"
	"github.com/soulteary/gorge-search/internal/esquery"
)

func TestNew_DefaultValues(t *testing.T) {
	se := New([]config.BackendDef{
		{
			Type:  "elasticsearch",
			Hosts: []string{"es:9200"},
		},
	})

	if !se.HasBackends() {
		t.Fatal("expected backends")
	}

	b := se.backends[0]
	if b.Index != "phabricator" {
		t.Fatalf("expected index phabricator, got %s", b.Index)
	}
	if b.Version != 5 {
		t.Fatalf("expected version 5, got %d", b.Version)
	}
	if b.Timeout != 15 {
		t.Fatalf("expected timeout 15, got %d", b.Timeout)
	}
	if !b.Roles["read"] || !b.Roles["write"] {
		t.Fatal("expected read+write roles by default")
	}
}

func TestBuildSearchSpec_BasicQuery(t *testing.T) {
	b := &Backend{Version: 5}
	q := &SearchQuery{
		Query: "hello world",
		Limit: 25,
	}
	spec := buildSearchSpec(b, q)

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	specStr := string(data)

	if !jsonContains(specStr, `"simple_query_string"`) {
		t.Fatal("expected simple_query_string in spec")
	}
	if !jsonContains(specStr, `"hello world"`) {
		t.Fatal("expected query text in spec")
	}
	if !jsonContains(specStr, `"AND"`) {
		t.Fatal("expected AND default_operator")
	}
}

func TestBuildSearchSpec_NoQuery_MatchAll(t *testing.T) {
	b := &Backend{Version: 5}
	q := &SearchQuery{}
	spec := buildSearchSpec(b, q)

	data, _ := json.Marshal(spec)
	specStr := string(data)

	if !jsonContains(specStr, `"match_all"`) {
		t.Fatal("expected match_all for empty query")
	}
	if !jsonContains(specStr, `"dateCreated"`) {
		t.Fatal("expected dateCreated sort for empty query")
	}
}

func TestBuildSearchSpec_WithFilters(t *testing.T) {
	b := &Backend{Version: 5}
	q := &SearchQuery{
		Query:       "test",
		AuthorPHIDs: []string{"PHID-USER-1"},
		Statuses:    []string{esquery.RelOpen},
	}
	spec := buildSearchSpec(b, q)

	data, _ := json.Marshal(spec)
	specStr := string(data)

	if !jsonContains(specStr, `"PHID-USER-1"`) {
		t.Fatal("expected author PHID filter")
	}
	if !jsonContains(specStr, `"open"`) {
		t.Fatal("expected open status filter")
	}
}

func TestBuildSearchSpec_MaxOffset(t *testing.T) {
	b := &Backend{Version: 5}
	q := &SearchQuery{
		Offset: 9999,
		Limit:  100,
	}
	spec := buildSearchSpec(b, q)

	from, ok := spec["from"].(int)
	if !ok {
		t.Fatal("expected from to be int")
	}
	size, ok := spec["size"].(int)
	if !ok {
		t.Fatal("expected size to be int")
	}
	if from+size > 10000 {
		t.Fatalf("offset+limit exceeds 10000: %d+%d=%d", from, size, from+size)
	}
}

func TestBuildDocSpec(t *testing.T) {
	b := &Backend{Version: 5}
	doc := &Document{
		PHID:         "PHID-TASK-123",
		Type:         "TASK",
		Title:        "Test Task",
		DateCreated:  1700000000,
		DateModified: 1700000100,
		Fields: []DocumentField{
			{Name: "titl", Corpus: "Test Task"},
			{Name: "body", Corpus: "Task body text"},
		},
		Relationships: []DocumentRelation{
			{Name: "auth", RelatedPHID: "PHID-USER-1"},
		},
	}

	spec := buildDocSpec(b, doc)

	if spec["title"] != "Test Task" {
		t.Fatalf("expected title 'Test Task', got %v", spec["title"])
	}
	if spec["lastModified"] != int64(1700000100) {
		t.Fatalf("expected lastModified 1700000100, got %v", spec["lastModified"])
	}
}

func TestBuildIndexConfig(t *testing.T) {
	b := &Backend{Version: 5}
	cfg := buildIndexConfig(b, []string{"TASK", "DREV"})

	settings, ok := cfg["settings"].(map[string]any)
	if !ok {
		t.Fatal("missing settings")
	}
	indexSettings, ok := settings["index"].(map[string]any)
	if !ok {
		t.Fatal("missing index settings")
	}
	analysis, ok := indexSettings["analysis"].(map[string]any)
	if !ok {
		t.Fatal("missing analysis")
	}
	analyzers, ok := analysis["analyzer"].(map[string]any)
	if !ok {
		t.Fatal("missing analyzers")
	}
	if _, ok := analyzers["english_exact"]; !ok {
		t.Fatal("missing english_exact analyzer")
	}

	mappings, ok := cfg["mappings"].(map[string]any)
	if !ok {
		t.Fatal("missing mappings")
	}
	if _, ok := mappings["TASK"]; !ok {
		t.Fatal("missing TASK mapping")
	}
	if _, ok := mappings["DREV"]; !ok {
		t.Fatal("missing DREV mapping")
	}
}

func TestConfigDeepMatch(t *testing.T) {
	actual := map[string]any{
		"settings": map[string]any{
			"index": map[string]any{
				"auto_expand_replicas": "0-2",
				"number_of_shards":     "1",
			},
		},
		"mappings": map[string]any{
			"TASK": map[string]any{
				"properties": map[string]any{
					"titl": map[string]any{"type": "text"},
				},
			},
		},
	}

	required := map[string]any{
		"settings": map[string]any{
			"index": map[string]any{
				"auto_expand_replicas": "0-2",
			},
		},
	}

	if !configDeepMatch(actual, required) {
		t.Fatal("expected match when required is subset of actual")
	}

	requiredBad := map[string]any{
		"settings": map[string]any{
			"index": map[string]any{
				"auto_expand_replicas": "1-3",
			},
		},
	}
	if configDeepMatch(actual, requiredBad) {
		t.Fatal("expected no match when values differ")
	}

	requiredMissing := map[string]any{
		"settings": map[string]any{
			"index": map[string]any{
				"nonexistent_key": "value",
			},
		},
	}
	if configDeepMatch(actual, requiredMissing) {
		t.Fatal("expected no match when key is missing from actual")
	}

	requiredAll := map[string]any{
		"_all": map[string]any{"enabled": true},
	}
	if !configDeepMatch(actual, requiredAll) {
		t.Fatal("expected _all key to be skipped")
	}
}

func TestNormalizeConfigValue(t *testing.T) {
	if normalizeConfigValue(true) != "true" {
		t.Fatal("expected true -> 'true'")
	}
	if normalizeConfigValue(false) != "false" {
		t.Fatal("expected false -> 'false'")
	}
	if normalizeConfigValue("hello") != "hello" {
		t.Fatal("expected string passthrough")
	}
	if normalizeConfigValue(float64(5)) != "5" {
		t.Fatal("expected float64(5) -> '5'")
	}
}

func TestBackendInfo(t *testing.T) {
	se := New([]config.BackendDef{
		{
			Type:  "elasticsearch",
			Hosts: []string{"es:9200"},
			Roles: []string{"read", "write"},
		},
	})
	info := se.BackendInfo()
	if len(info) != 1 {
		t.Fatalf("expected 1 backend info, got %d", len(info))
	}
	if info[0]["type"] != "elasticsearch" {
		t.Fatalf("expected type elasticsearch, got %v", info[0]["type"])
	}
}

func jsonContains(s, substr string) bool {
	return len(s) > 0 && contains(s, substr)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
