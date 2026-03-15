package engine

// SearchBackend abstracts a fulltext search backend (Elasticsearch, Meilisearch, etc.).
// Each implementation handles its own protocol, query building, and index management.
type SearchBackend interface {
	Type() string
	HasRole(role string) bool

	IndexDocument(doc *Document) error
	Search(q *SearchQuery) ([]string, error)
	IndexExists() (bool, error)
	InitIndex(docTypes []string) error
	IndexStats() (map[string]any, error)
	IndexIsSane(docTypes []string) (bool, error)

	Info() map[string]any
}
