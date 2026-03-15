package config

import (
	"os"
	"testing"
)

func TestLoadFromEnv_Defaults(t *testing.T) {
	os.Clearenv()
	cfg := LoadFromEnv()

	if cfg.ListenAddr != ":8120" {
		t.Fatalf("expected :8120, got %s", cfg.ListenAddr)
	}
	if len(cfg.Backends) != 0 {
		t.Fatalf("expected 0 backends, got %d", len(cfg.Backends))
	}
}

func TestLoadFromEnv_ESFromEnv(t *testing.T) {
	os.Clearenv()
	t.Setenv("ES_HOST", "es1:9200,es2:9200")
	t.Setenv("ES_INDEX", "myindex")
	t.Setenv("ES_VERSION", "7")

	cfg := LoadFromEnv()

	if len(cfg.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(cfg.Backends))
	}
	b := cfg.Backends[0]
	if b.Type != "elasticsearch" {
		t.Fatalf("expected elasticsearch, got %s", b.Type)
	}
	if len(b.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(b.Hosts))
	}
	if b.Index != "myindex" {
		t.Fatalf("expected myindex, got %s", b.Index)
	}
	if b.Version != 7 {
		t.Fatalf("expected version 7, got %d", b.Version)
	}
}

func TestLoadFromEnv_JSONBackends(t *testing.T) {
	os.Clearenv()
	t.Setenv("SEARCH_BACKENDS", `[{"type":"elasticsearch","hosts":["es:9200"],"index":"phabricator","version":5}]`)

	cfg := LoadFromEnv()

	if len(cfg.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(cfg.Backends))
	}
	if cfg.Backends[0].Hosts[0] != "es:9200" {
		t.Fatalf("expected es:9200, got %s", cfg.Backends[0].Hosts[0])
	}
}
