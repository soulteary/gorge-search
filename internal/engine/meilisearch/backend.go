package meilisearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/soulteary/gorge-search/internal/config"
	"github.com/soulteary/gorge-search/internal/engine"
	"github.com/soulteary/gorge-search/internal/esquery"
)

// Backend implements engine.SearchBackend for Meilisearch.
//
// Meilisearch uses a single-node model (no sharding), so "hosts" contains
// one or more replicas behind a load-balancer. The first healthy host is
// used for each request.
//
// Key mapping from Phorge/ES concepts:
//   - ES index  -> Meilisearch index (uid)
//   - ES type   -> stored as a filterable "docType" attribute
//   - ES _id    -> Meilisearch document primary key "id" (= PHID)
//   - ES fields -> flattened into top-level Meilisearch attributes
type Backend struct {
	host    string
	index   string
	apiKey  string
	timeout int
	roles   map[string]bool
	client  *http.Client
}

func New(def config.BackendDef) *Backend {
	b := &Backend{
		index: def.Index,
		roles: make(map[string]bool),
	}
	if b.index == "" {
		b.index = "phabricator"
	}
	if len(def.Hosts) > 0 {
		host := def.Hosts[0]
		if !strings.Contains(host, "://") {
			proto := def.Protocol
			if proto == "" {
				proto = "http"
			}
			host = proto + "://" + host
		}
		b.host = strings.TrimRight(host, "/")
	}
	b.apiKey = def.APIKey
	b.timeout = def.Timeout
	if b.timeout == 0 {
		b.timeout = 15
	}
	for _, r := range def.Roles {
		b.roles[r] = true
	}
	if len(b.roles) == 0 {
		b.roles["read"] = true
		b.roles["write"] = true
	}
	b.client = &http.Client{Timeout: time.Duration(b.timeout) * time.Second}
	return b
}

func (b *Backend) Type() string { return "meilisearch" }

func (b *Backend) HasRole(role string) bool { return b.roles[role] }

func (b *Backend) Info() map[string]any {
	roles := make([]string, 0, len(b.roles))
	for r := range b.roles {
		roles = append(roles, r)
	}
	return map[string]any{
		"type":  b.Type(),
		"host":  b.host,
		"index": b.index,
		"roles": roles,
	}
}

func (b *Backend) IndexDocument(doc *engine.Document) error {
	msDoc := b.buildDocument(doc)
	body, err := json.Marshal([]any{msDoc})
	if err != nil {
		return fmt.Errorf("marshal document: %w", err)
	}
	url := fmt.Sprintf("%s/indexes/%s/documents", b.host, b.index)
	_, err = b.doRequest(url, http.MethodPost, bytes.NewReader(body))
	return err
}

func (b *Backend) Search(q *engine.SearchQuery) ([]string, error) {
	req := b.buildSearchRequest(q)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	url := fmt.Sprintf("%s/indexes/%s/search", b.host, b.index)
	respBody, err := b.doRequest(url, http.MethodPost, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var resp msSearchResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON from meilisearch: %w", err)
	}

	phids := make([]string, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		if id, ok := hit["id"].(string); ok {
			phids = append(phids, id)
		}
	}
	return phids, nil
}

func (b *Backend) IndexExists() (bool, error) {
	url := fmt.Sprintf("%s/indexes/%s", b.host, b.index)
	_, err := b.doRequest(url, http.MethodGet, nil)
	if err != nil {
		if strings.Contains(err.Error(), "status 404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *Backend) InitIndex(docTypes []string) error {
	_ = b.deleteIndex()

	createBody, _ := json.Marshal(map[string]string{
		"uid":        b.index,
		"primaryKey": "id",
	})
	url := fmt.Sprintf("%s/indexes", b.host)
	_, err := b.doRequest(url, http.MethodPost, bytes.NewReader(createBody))
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	if err := b.waitForIdle(); err != nil {
		return err
	}

	return b.configureIndex()
}

func (b *Backend) IndexStats() (map[string]any, error) {
	url := fmt.Sprintf("%s/indexes/%s/stats", b.host, b.index)
	body, err := b.doRequest(url, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}

	var stats msIndexStats
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("invalid stats JSON: %w", err)
	}

	return map[string]any{
		"documents": stats.NumberOfDocuments,
		"indexing":  stats.IsIndexing,
	}, nil
}

func (b *Backend) IndexIsSane(_ []string) (bool, error) {
	exists, err := b.IndexExists()
	if err != nil || !exists {
		return false, err
	}

	url := fmt.Sprintf("%s/indexes/%s/settings", b.host, b.index)
	body, err := b.doRequest(url, http.MethodGet, nil)
	if err != nil {
		return false, err
	}

	var settings msSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return false, fmt.Errorf("invalid settings JSON: %w", err)
	}

	expectedSearchable := b.searchableAttributes()
	if len(settings.SearchableAttributes) != len(expectedSearchable) {
		return false, nil
	}

	expectedFilterable := b.filterableAttributes()
	filterSet := make(map[string]bool, len(settings.FilterableAttributes))
	for _, f := range settings.FilterableAttributes {
		filterSet[f] = true
	}
	for _, f := range expectedFilterable {
		if !filterSet[f] {
			return false, nil
		}
	}

	return true, nil
}

// Internal helpers

func (b *Backend) deleteIndex() error {
	url := fmt.Sprintf("%s/indexes/%s", b.host, b.index)
	_, err := b.doRequest(url, http.MethodDelete, nil)
	return err
}

func (b *Backend) configureIndex() error {
	settings := msSettings{
		SearchableAttributes: b.searchableAttributes(),
		FilterableAttributes: b.filterableAttributes(),
		SortableAttributes:   b.sortableAttributes(),
		DisplayedAttributes:  []string{"id", "docType"},
		RankingRules: []string{
			"words", "typo", "proximity", "attribute", "sort", "exactness",
		},
	}
	body, _ := json.Marshal(settings)
	url := fmt.Sprintf("%s/indexes/%s/settings", b.host, b.index)
	_, err := b.doRequest(url, http.MethodPatch, bytes.NewReader(body))
	return err
}

func (b *Backend) searchableAttributes() []string {
	fields := esquery.AllFields()
	attrs := make([]string, 0, len(fields)+1)
	attrs = append(attrs, "title")
	attrs = append(attrs, fields...)
	return attrs
}

func (b *Backend) filterableAttributes() []string {
	rels := esquery.AllRelationships()
	attrs := make([]string, 0, len(rels)+2)
	attrs = append(attrs, "docType")
	attrs = append(attrs, rels...)
	return attrs
}

func (b *Backend) sortableAttributes() []string {
	return []string{"dateCreated", "lastModified"}
}

func (b *Backend) waitForIdle() error {
	for i := 0; i < 30; i++ {
		time.Sleep(200 * time.Millisecond)
		url := fmt.Sprintf("%s/tasks?statuses=enqueued,processing&limit=1", b.host)
		body, err := b.doRequest(url, http.MethodGet, nil)
		if err != nil {
			continue
		}
		var resp struct {
			Total int `json:"total"`
		}
		if json.Unmarshal(body, &resp) == nil && resp.Total == 0 {
			return nil
		}
	}
	return nil
}

func (b *Backend) buildDocument(doc *engine.Document) map[string]any {
	m := map[string]any{
		"id":           doc.PHID,
		"docType":      doc.Type,
		"title":        doc.Title,
		"dateCreated":  doc.DateCreated,
		"lastModified": doc.DateModified,
	}

	for _, f := range doc.Fields {
		key := f.Name
		existing, ok := m[key]
		if ok {
			if arr, isArr := existing.([]any); isArr {
				arr = append(arr, f.Corpus)
				if f.Aux != "" {
					arr = append(arr, f.Aux)
				}
				m[key] = arr
			} else {
				vals := []any{existing, f.Corpus}
				if f.Aux != "" {
					vals = append(vals, f.Aux)
				}
				m[key] = vals
			}
		} else {
			if f.Aux != "" {
				m[key] = f.Corpus + " " + f.Aux
			} else {
				m[key] = f.Corpus
			}
		}
	}

	for _, r := range doc.Relationships {
		key := r.Name
		existing, ok := m[key]
		if ok {
			if arr, isArr := existing.([]any); isArr {
				m[key] = append(arr, r.RelatedPHID)
			} else {
				m[key] = []any{existing, r.RelatedPHID}
			}
		} else {
			m[key] = []any{r.RelatedPHID}
		}
	}

	return m
}

func (b *Backend) buildSearchRequest(q *engine.SearchQuery) map[string]any {
	req := map[string]any{
		"q":                    q.Query,
		"attributesToRetrieve": []string{"id"},
	}

	limit := q.Limit
	if limit == 0 {
		limit = 101
	}
	req["limit"] = limit
	req["offset"] = q.Offset

	filters := b.buildFilters(q)
	if len(filters) > 0 {
		req["filter"] = filters
	}

	if q.Query == "" {
		req["sort"] = []string{"dateCreated:desc"}
	}

	return req
}

func (b *Backend) buildFilters(q *engine.SearchQuery) []any {
	var filters []any

	if len(q.Types) > 0 {
		typeFilters := make([]string, 0, len(q.Types))
		for _, t := range q.Types {
			typeFilters = append(typeFilters, fmt.Sprintf("docType = %s", t))
		}
		if len(typeFilters) == 1 {
			filters = append(filters, typeFilters[0])
		} else {
			filters = append(filters, typeFilters)
		}
	}

	if q.Exclude != "" {
		filters = append(filters, fmt.Sprintf("id != %s", q.Exclude))
	}

	relMap := map[string][]string{
		esquery.RelAuthor:     q.AuthorPHIDs,
		esquery.RelSubscriber: q.SubscriberPHIDs,
		esquery.RelProject:    q.ProjectPHIDs,
		esquery.RelRepository: q.RepositoryPHIDs,
	}
	for field, phids := range relMap {
		for _, phid := range phids {
			filters = append(filters, fmt.Sprintf("%s = %s", field, phid))
		}
	}

	statusSet := make(map[string]bool, len(q.Statuses))
	for _, s := range q.Statuses {
		statusSet[s] = true
	}
	if statusSet[esquery.RelOpen] && !statusSet[esquery.RelClosed] {
		filters = append(filters, fmt.Sprintf("%s EXISTS", esquery.RelOpen))
	} else if !statusSet[esquery.RelOpen] && statusSet[esquery.RelClosed] {
		filters = append(filters, fmt.Sprintf("%s EXISTS", esquery.RelClosed))
	}

	if q.WithUnowned {
		filters = append(filters, fmt.Sprintf("%s EXISTS", esquery.RelUnowned))
	}

	if q.WithAnyOwner {
		filters = append(filters, fmt.Sprintf("%s EXISTS", esquery.RelOwner))
	} else if len(q.OwnerPHIDs) > 0 {
		for _, phid := range q.OwnerPHIDs {
			filters = append(filters, fmt.Sprintf("%s = %s", esquery.RelOwner, phid))
		}
	}

	return filters
}

// HTTP layer

func (b *Backend) doRequest(url, method string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meilisearch request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("meilisearch returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// Response types

type msSearchResponse struct {
	Hits []map[string]any `json:"hits"`
}

type msIndexStats struct {
	NumberOfDocuments int  `json:"numberOfDocuments"`
	IsIndexing        bool `json:"isIndexing"`
}

type msSettings struct {
	SearchableAttributes []string `json:"searchableAttributes,omitempty"`
	FilterableAttributes []string `json:"filterableAttributes,omitempty"`
	SortableAttributes   []string `json:"sortableAttributes,omitempty"`
	DisplayedAttributes  []string `json:"displayedAttributes,omitempty"`
	RankingRules         []string `json:"rankingRules,omitempty"`
}
