package engine

import (
	"fmt"
	"testing"
)

type mockBackend struct {
	backendType string
	roles       map[string]bool
	docs        map[string]*Document
	indexInited bool
}

func newMockBackend(roles ...string) *mockBackend {
	m := &mockBackend{
		backendType: "mock",
		roles:       make(map[string]bool),
		docs:        make(map[string]*Document),
	}
	for _, r := range roles {
		m.roles[r] = true
	}
	if len(m.roles) == 0 {
		m.roles["read"] = true
		m.roles["write"] = true
	}
	return m
}

func (m *mockBackend) Type() string             { return m.backendType }
func (m *mockBackend) HasRole(role string) bool { return m.roles[role] }

func (m *mockBackend) IndexDocument(doc *Document) error {
	m.docs[doc.PHID] = doc
	return nil
}

func (m *mockBackend) Search(q *SearchQuery) ([]string, error) {
	var phids []string
	for phid := range m.docs {
		phids = append(phids, phid)
	}
	return phids, nil
}

func (m *mockBackend) IndexExists() (bool, error) {
	return m.indexInited, nil
}

func (m *mockBackend) InitIndex(_ []string) error {
	m.indexInited = true
	m.docs = make(map[string]*Document)
	return nil
}

func (m *mockBackend) IndexStats() (map[string]any, error) {
	return map[string]any{"documents": len(m.docs)}, nil
}

func (m *mockBackend) IndexIsSane(_ []string) (bool, error) {
	return m.indexInited, nil
}

func (m *mockBackend) Info() map[string]any {
	return map[string]any{"type": m.backendType}
}

func TestNew_WithBackends(t *testing.T) {
	se := New([]SearchBackend{newMockBackend()})
	if !se.HasBackends() {
		t.Fatal("expected backends")
	}
}

func TestNew_NoBackends(t *testing.T) {
	se := New(nil)
	if se.HasBackends() {
		t.Fatal("expected no backends")
	}
}

func TestIndexDocument(t *testing.T) {
	mb := newMockBackend("read", "write")
	se := New([]SearchBackend{mb})

	doc := &Document{PHID: "PHID-TASK-1", Type: "TASK", Title: "Test"}
	if err := se.IndexDocument(doc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := mb.docs["PHID-TASK-1"]; !ok {
		t.Fatal("document not indexed")
	}
}

func TestSearch(t *testing.T) {
	mb := newMockBackend("read", "write")
	mb.docs["PHID-TASK-1"] = &Document{PHID: "PHID-TASK-1"}
	se := New([]SearchBackend{mb})

	phids, err := se.Search(&SearchQuery{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(phids) != 1 {
		t.Fatalf("expected 1 result, got %d", len(phids))
	}
}

func TestSearch_NoReadableBackends(t *testing.T) {
	mb := newMockBackend("write")
	se := New([]SearchBackend{mb})

	_, err := se.Search(&SearchQuery{Query: "test"})
	if err == nil {
		t.Fatal("expected error for no readable backends")
	}
}

func TestInitIndex(t *testing.T) {
	mb := newMockBackend("read", "write")
	se := New([]SearchBackend{mb})

	if err := se.InitIndex([]string{"TASK"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mb.indexInited {
		t.Fatal("index not initialized")
	}
}

func TestIndexExists(t *testing.T) {
	mb := newMockBackend("read", "write")
	se := New([]SearchBackend{mb})

	exists, err := se.IndexExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected index to not exist initially")
	}

	mb.indexInited = true
	exists, err = se.IndexExists()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected index to exist after init")
	}
}

func TestBackendInfo(t *testing.T) {
	mb := newMockBackend("read", "write")
	se := New([]SearchBackend{mb})

	info := se.BackendInfo()
	if len(info) != 1 {
		t.Fatalf("expected 1 backend info, got %d", len(info))
	}
	if info[0]["type"] != "mock" {
		t.Fatalf("expected type mock, got %v", info[0]["type"])
	}
}

func TestMultipleBackends_Failover(t *testing.T) {
	failing := &failingBackend{roles: map[string]bool{"read": true}}
	working := newMockBackend("read")
	working.docs["PHID-TASK-1"] = &Document{PHID: "PHID-TASK-1"}

	se := New([]SearchBackend{failing, working})
	phids, err := se.Search(&SearchQuery{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(phids) != 1 {
		t.Fatalf("expected 1 result from failover, got %d", len(phids))
	}
}

type failingBackend struct {
	roles map[string]bool
}

func (f *failingBackend) Type() string             { return "failing" }
func (f *failingBackend) HasRole(role string) bool { return f.roles[role] }
func (f *failingBackend) IndexDocument(_ *Document) error {
	return fmt.Errorf("always fails")
}
func (f *failingBackend) Search(_ *SearchQuery) ([]string, error) {
	return nil, fmt.Errorf("always fails")
}
func (f *failingBackend) IndexExists() (bool, error) { return false, fmt.Errorf("always fails") }
func (f *failingBackend) InitIndex(_ []string) error { return fmt.Errorf("always fails") }
func (f *failingBackend) IndexStats() (map[string]any, error) {
	return nil, fmt.Errorf("always fails")
}
func (f *failingBackend) IndexIsSane(_ []string) (bool, error) {
	return false, fmt.Errorf("always fails")
}
func (f *failingBackend) Info() map[string]any { return map[string]any{"type": "failing"} }
