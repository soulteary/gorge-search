package engine

import "fmt"

// SearchEngine manages one or more search backends, dispatching
// operations to the appropriate backend by role.
type SearchEngine struct {
	backends []SearchBackend
}

func New(backends []SearchBackend) *SearchEngine {
	return &SearchEngine{backends: backends}
}

func (se *SearchEngine) HasBackends() bool {
	return len(se.backends) > 0
}

func (se *SearchEngine) BackendInfo() []map[string]any {
	result := make([]map[string]any, 0, len(se.backends))
	for _, b := range se.backends {
		result = append(result, b.Info())
	}
	return result
}

func (se *SearchEngine) IndexDocument(doc *Document) error {
	var lastErr error
	for _, b := range se.backends {
		if !b.HasRole("write") {
			continue
		}
		if err := b.IndexDocument(doc); err != nil {
			lastErr = err
			continue
		}
	}
	return lastErr
}

func (se *SearchEngine) Search(q *SearchQuery) ([]string, error) {
	var lastErr error
	for _, b := range se.backends {
		if !b.HasRole("read") {
			continue
		}
		phids, err := b.Search(q)
		if err != nil {
			lastErr = err
			continue
		}
		return phids, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("all fulltext search backends failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no readable search backends configured")
}

func (se *SearchEngine) IndexExists() (bool, error) {
	for _, b := range se.backends {
		if !b.HasRole("read") {
			continue
		}
		return b.IndexExists()
	}
	return false, fmt.Errorf("no readable search backends")
}

func (se *SearchEngine) InitIndex(docTypes []string) error {
	var lastErr error
	for _, b := range se.backends {
		if !b.HasRole("write") {
			continue
		}
		if err := b.InitIndex(docTypes); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (se *SearchEngine) IndexStats() (map[string]any, error) {
	for _, b := range se.backends {
		if !b.HasRole("read") {
			continue
		}
		stats, err := b.IndexStats()
		if err != nil {
			continue
		}
		return stats, nil
	}
	return nil, fmt.Errorf("no stats available")
}

func (se *SearchEngine) IndexIsSane(docTypes []string) (bool, error) {
	for _, b := range se.backends {
		if !b.HasRole("read") {
			continue
		}
		return b.IndexIsSane(docTypes)
	}
	return false, fmt.Errorf("could not verify index sanity")
}
