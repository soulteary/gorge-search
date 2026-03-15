package elasticsearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/soulteary/gorge-search/internal/config"
	"github.com/soulteary/gorge-search/internal/engine"
	"github.com/soulteary/gorge-search/internal/esquery"
)

type Backend struct {
	hosts    []string
	index    string
	version  int
	timeout  int
	protocol string
	roles    map[string]bool

	mu     sync.RWMutex
	health map[string]bool
	client *http.Client
}

func New(def config.BackendDef) *Backend {
	b := &Backend{
		hosts:    def.Hosts,
		index:    strings.ReplaceAll(def.Index, "/", ""),
		version:  def.Version,
		timeout:  def.Timeout,
		protocol: def.Protocol,
		roles:    make(map[string]bool),
		health:   make(map[string]bool),
	}
	if b.index == "" {
		b.index = "phabricator"
	}
	if b.version == 0 {
		b.version = 5
	}
	if b.timeout == 0 {
		b.timeout = 15
	}
	if b.protocol == "" {
		b.protocol = "http"
	}
	for _, r := range def.Roles {
		b.roles[r] = true
	}
	if len(b.roles) == 0 {
		b.roles["read"] = true
		b.roles["write"] = true
	}
	for _, h := range b.hosts {
		b.health[h] = true
	}
	b.client = &http.Client{Timeout: time.Duration(b.timeout) * time.Second}
	return b
}

func (b *Backend) Type() string { return "elasticsearch" }

func (b *Backend) HasRole(role string) bool { return b.roles[role] }

func (b *Backend) Info() map[string]any {
	roles := make([]string, 0, len(b.roles))
	for r := range b.roles {
		roles = append(roles, r)
	}
	return map[string]any{
		"type":    b.Type(),
		"hosts":   b.hosts,
		"index":   b.index,
		"version": b.version,
		"roles":   roles,
	}
}

func (b *Backend) hostForRole(role string) (string, error) {
	if !b.roles[role] {
		return "", fmt.Errorf("backend does not have role %q", role)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	healthy := make([]string, 0, len(b.hosts))
	for _, h := range b.hosts {
		if b.health[h] {
			healthy = append(healthy, h)
		}
	}
	if len(healthy) == 0 {
		return "", fmt.Errorf("no healthy hosts for role %q", role)
	}
	return healthy[rand.Intn(len(healthy))], nil
}

func (b *Backend) allHostsForRole(role string) []string {
	if !b.roles[role] {
		return nil
	}
	return b.hosts
}

func (b *Backend) markHealth(host string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.health[host] = ok
}

func (b *Backend) baseURL(host string) string {
	proto := b.protocol
	if proto == "" {
		proto = "http"
	}
	if strings.Contains(host, "://") {
		return strings.TrimRight(host, "/") + "/" + b.index
	}
	return fmt.Sprintf("%s://%s/%s", proto, host, b.index)
}

func (b *Backend) timestampField() string {
	if b.version < 2 {
		return "_timestamp"
	}
	return "lastModified"
}

func (b *Backend) textFieldType() string {
	if b.version >= 5 {
		return "text"
	}
	return "string"
}

func (b *Backend) IndexDocument(doc *engine.Document) error {
	host, err := b.hostForRole("write")
	if err != nil {
		return err
	}
	spec := b.buildDocSpec(doc)
	url := fmt.Sprintf("%s/%s/%s", b.baseURL(host), doc.Type, doc.PHID)
	return b.doRequest(host, url, http.MethodPut, spec)
}

func (b *Backend) Search(q *engine.SearchQuery) ([]string, error) {
	hosts := b.allHostsForRole("read")
	if len(hosts) == 0 {
		return nil, fmt.Errorf("backend has no read role")
	}

	spec := b.buildSearchSpec(q)

	var lastErr error
	for _, host := range hosts {
		types := q.Types
		var uri string
		if len(types) > 0 {
			uri = fmt.Sprintf("%s/%s/_search", b.baseURL(host), strings.Join(types, ","))
		} else {
			uri = fmt.Sprintf("%s/_search", b.baseURL(host))
		}

		body, err := b.doRequestRead(host, uri, http.MethodPost, spec)
		if err != nil {
			lastErr = err
			continue
		}

		var resp esSearchResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			b.markHealth(host, false)
			lastErr = fmt.Errorf("invalid JSON from elasticsearch: %w", err)
			continue
		}

		phids := make([]string, 0, len(resp.Hits.Hits))
		for _, h := range resp.Hits.Hits {
			phids = append(phids, h.ID)
		}
		return phids, nil
	}
	return nil, fmt.Errorf("all elasticsearch hosts failed: %w", lastErr)
}

func (b *Backend) IndexExists() (bool, error) {
	host, err := b.hostForRole("read")
	if err != nil {
		return false, err
	}

	if b.version >= 5 {
		url := fmt.Sprintf("%s/_stats/", b.baseURL(host))
		body, err := b.doRequestRead(host, url, http.MethodGet, nil)
		if err != nil {
			return false, err
		}
		var stats map[string]any
		if err := json.Unmarshal(body, &stats); err != nil {
			return false, err
		}
		indices, _ := stats["indices"].(map[string]any)
		_, exists := indices[b.index]
		return exists, nil
	}

	url := b.baseURL(host) + "/_status/"
	_, err = b.doRequestRead(host, url, http.MethodGet, nil)
	return err == nil, nil
}

func (b *Backend) InitIndex(docTypes []string) error {
	host, err := b.hostForRole("write")
	if err != nil {
		return err
	}

	url := b.baseURL(host)
	_ = b.doRequest(host, url, http.MethodDelete, nil)

	data := b.buildIndexConfig(docTypes)
	return b.doRequest(host, url, http.MethodPut, data)
}

func (b *Backend) IndexStats() (map[string]any, error) {
	if b.version < 2 {
		return nil, fmt.Errorf("stats not supported for version %d", b.version)
	}
	host, err := b.hostForRole("read")
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/_stats/", b.baseURL(host))
	body, err := b.doRequestRead(host, url, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	indices, _ := raw["indices"].(map[string]any)
	idx, _ := indices[b.index].(map[string]any)
	if idx == nil {
		return nil, fmt.Errorf("index %q not found in stats", b.index)
	}

	return map[string]any{
		"queries":       jsonPath(idx, "primaries", "search", "query_total"),
		"documents":     jsonPath(idx, "total", "docs", "count"),
		"deleted":       jsonPath(idx, "total", "docs", "deleted"),
		"storage_bytes": jsonPath(idx, "total", "store", "size_in_bytes"),
	}, nil
}

func (b *Backend) IndexIsSane(docTypes []string) (bool, error) {
	exists, err := b.IndexExists()
	if err != nil || !exists {
		return false, err
	}

	host, err := b.hostForRole("read")
	if err != nil {
		return false, err
	}

	mappingURL := fmt.Sprintf("%s/_mapping/", b.baseURL(host))
	mappingBody, err := b.doRequestRead(host, mappingURL, http.MethodGet, nil)
	if err != nil {
		return false, err
	}

	settingsURL := fmt.Sprintf("%s/_settings/", b.baseURL(host))
	settingsBody, err := b.doRequestRead(host, settingsURL, http.MethodGet, nil)
	if err != nil {
		return false, err
	}

	var mappingResp, settingsResp map[string]any
	if json.Unmarshal(mappingBody, &mappingResp) != nil {
		return false, fmt.Errorf("invalid mapping response")
	}
	if json.Unmarshal(settingsBody, &settingsResp) != nil {
		return false, fmt.Errorf("invalid settings response")
	}

	actual := mergeAny(
		asMap(settingsResp[b.index]),
		asMap(mappingResp[b.index]),
	)

	expected := b.buildIndexConfig(docTypes)
	return configDeepMatch(actual, expected), nil
}

// HTTP helpers

func (b *Backend) doRequest(host, url, method string, body any) error {
	_, err := b.doRequestRead(host, url, method, body)
	return err
}

func (b *Backend) doRequestRead(host, url, method string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		b.markHealth(host, false)
		return nil, fmt.Errorf("elasticsearch request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		b.markHealth(host, false)
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 500 {
		b.markHealth(host, false)
		return nil, fmt.Errorf("elasticsearch returned status %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("elasticsearch returned status %d: %s", resp.StatusCode, string(respBody))
	}

	b.markHealth(host, true)
	return respBody, nil
}

type esSearchResponse struct {
	Hits struct {
		Hits []struct {
			ID string `json:"_id"`
		} `json:"hits"`
	} `json:"hits"`
}

// Query/Index building

func (b *Backend) buildDocSpec(doc *engine.Document) map[string]any {
	ts := b.timestampField()
	spec := map[string]any{
		"title":       doc.Title,
		"dateCreated": doc.DateCreated,
		ts:            doc.DateModified,
	}

	for _, f := range doc.Fields {
		key := f.Name
		existing, ok := spec[key]
		if ok {
			if arr, isArr := existing.([]any); isArr {
				arr = append(arr, f.Corpus)
				if f.Aux != "" {
					arr = append(arr, f.Aux)
				}
				spec[key] = arr
			} else {
				vals := []any{existing, f.Corpus}
				if f.Aux != "" {
					vals = append(vals, f.Aux)
				}
				spec[key] = vals
			}
		} else {
			vals := []any{f.Corpus}
			if f.Aux != "" {
				vals = append(vals, f.Aux)
			}
			spec[key] = vals
		}
	}

	for _, r := range doc.Relationships {
		key := r.Name
		existing, ok := spec[key]
		if ok {
			if arr, isArr := existing.([]any); isArr {
				spec[key] = append(arr, r.RelatedPHID)
			} else {
				spec[key] = []any{existing, r.RelatedPHID}
			}
		} else {
			spec[key] = []any{r.RelatedPHID}
		}
		if r.Timestamp > 0 {
			spec[key+"_ts"] = r.Timestamp
		}
	}

	return spec
}

func (b *Backend) buildSearchSpec(q *engine.SearchQuery) map[string]any {
	bq := &esquery.BoolQuery{}

	if q.Query != "" {
		bq.AddMust(map[string]any{
			"simple_query_string": map[string]any{
				"query": q.Query,
				"fields": []string{
					esquery.FieldTitle + ".*",
					esquery.FieldBody + ".*",
					esquery.FieldComment + ".*",
				},
				"default_operator": "AND",
			},
		})

		bq.AddShould(map[string]any{
			"simple_query_string": map[string]any{
				"query": q.Query,
				"fields": []string{
					"*.raw",
					esquery.FieldTitle + "^4",
					esquery.FieldBody + "^3",
					esquery.FieldComment + "^1.2",
				},
				"analyzer":         "english_exact",
				"default_operator": "and",
			},
		})
	}

	if q.Exclude != "" {
		bq.AddFilter(map[string]any{
			"not": map[string]any{
				"ids": map[string]any{
					"values": []string{q.Exclude},
				},
			},
		})
	}

	relMap := map[string][]string{
		esquery.RelAuthor:     q.AuthorPHIDs,
		esquery.RelSubscriber: q.SubscriberPHIDs,
		esquery.RelProject:    q.ProjectPHIDs,
		esquery.RelRepository: q.RepositoryPHIDs,
	}
	for field, phids := range relMap {
		if len(phids) > 0 {
			bq.AddTerms(field, phids)
		}
	}

	statusSet := make(map[string]bool, len(q.Statuses))
	for _, s := range q.Statuses {
		statusSet[s] = true
	}
	includeOpen := statusSet[esquery.RelOpen]
	includeClosed := statusSet[esquery.RelClosed]
	if includeOpen && !includeClosed {
		bq.AddExists(esquery.RelOpen)
	} else if !includeOpen && includeClosed {
		bq.AddExists(esquery.RelClosed)
	}

	if q.WithUnowned {
		bq.AddExists(esquery.RelUnowned)
	}

	if q.WithAnyOwner {
		bq.AddExists(esquery.RelOwner)
	} else if len(q.OwnerPHIDs) > 0 {
		bq.AddTerms(esquery.RelOwner, q.OwnerPHIDs)
	}

	if bq.MustCount() == 0 {
		bq.AddMust(map[string]any{
			"match_all": map[string]any{"boost": 1},
		})
	}

	spec := map[string]any{
		"_source": false,
		"query": map[string]any{
			"bool": bq,
		},
	}

	if q.Query == "" {
		spec["sort"] = []any{
			map[string]string{"dateCreated": "desc"},
		}
	}

	offset := q.Offset
	limit := q.Limit
	if limit == 0 {
		limit = 101
	}
	if offset+limit > 10000 {
		offset = 10000 - limit
		if offset < 0 {
			offset = 0
		}
	}
	spec["from"] = offset
	spec["size"] = limit

	return spec
}

func (b *Backend) buildIndexConfig(docTypes []string) map[string]any {
	textType := b.textFieldType()

	data := map[string]any{
		"settings": map[string]any{
			"index": map[string]any{
				"auto_expand_replicas": "0-2",
				"analysis": map[string]any{
					"filter": map[string]any{
						"english_stop": map[string]any{
							"type":      "stop",
							"stopwords": "_english_",
						},
						"english_stemmer": map[string]any{
							"type":     "stemmer",
							"language": "english",
						},
						"english_possessive_stemmer": map[string]any{
							"type":     "stemmer",
							"language": "possessive_english",
						},
					},
					"analyzer": map[string]any{
						"english_exact": map[string]any{
							"tokenizer": "standard",
							"filter":    []string{"lowercase"},
						},
						"letter_stop": map[string]any{
							"tokenizer": "letter",
							"filter":    []string{"lowercase", "english_stop"},
						},
						"english_stem": map[string]any{
							"tokenizer": "standard",
							"filter": []string{
								"english_possessive_stemmer",
								"lowercase",
								"english_stop",
								"english_stemmer",
							},
						},
					},
				},
			},
		},
	}

	fields := esquery.AllFields()
	rels := esquery.AllRelationships()
	mappings := map[string]any{}

	for _, docType := range docTypes {
		props := map[string]any{}

		for _, f := range fields {
			props[f] = map[string]any{
				"type": textType,
				"fields": map[string]any{
					"raw": map[string]any{
						"type":                  textType,
						"analyzer":              "english_exact",
						"search_analyzer":       "english",
						"search_quote_analyzer": "english_exact",
					},
					"keywords": map[string]any{
						"type":     textType,
						"analyzer": "letter_stop",
					},
					"stems": map[string]any{
						"type":     textType,
						"analyzer": "english_stem",
					},
				},
			}
		}

		for _, rel := range rels {
			if b.version >= 5 {
				props[rel] = map[string]any{
					"type":           "keyword",
					"include_in_all": false,
					"doc_values":     false,
				}
			} else {
				props[rel] = map[string]any{
					"type":           "string",
					"index":          "not_analyzed",
					"include_in_all": false,
				}
			}
			props[rel+"_ts"] = map[string]any{
				"type":           "date",
				"include_in_all": false,
			}
		}

		props["dateCreated"] = map[string]any{"type": "date"}
		props["lastModified"] = map[string]any{"type": "date"}

		mappings[docType] = map[string]any{
			"properties": props,
		}
	}

	data["mappings"] = mappings
	return data
}

// Utility helpers

func jsonPath(m map[string]any, keys ...string) any {
	cur := any(m)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func configDeepMatch(actual, required map[string]any) bool {
	for key, rval := range required {
		aval, exists := actual[key]
		if !exists {
			if key == "_all" {
				continue
			}
			return false
		}

		rmap, rIsMap := rval.(map[string]any)
		if rIsMap {
			amap, aIsMap := aval.(map[string]any)
			if !aIsMap {
				return false
			}
			if !configDeepMatch(amap, rmap) {
				return false
			}
			continue
		}

		if normalizeConfigValue(aval) != normalizeConfigValue(rval) {
			return false
		}
	}
	return true
}

func normalizeConfigValue(v any) string {
	switch val := v.(type) {
	case bool:
		if val {
			return "true"
		}
		return "false"
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func mergeAny(a, b map[string]any) map[string]any {
	result := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		result[k] = v
	}
	for k, v := range b {
		result[k] = v
	}
	return result
}
