package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr   string       `json:"listenAddr"`
	ServiceToken string       `json:"serviceToken"`
	Backends     []BackendDef `json:"backends"`
}

type BackendDef struct {
	Type     string   `json:"type"`
	Hosts    []string `json:"hosts"`
	Index    string   `json:"index"`
	Version  int      `json:"version"`
	Roles    []string `json:"roles"`
	Timeout  int      `json:"timeout"`
	Protocol string   `json:"protocol"`
}

func LoadFromEnv() *Config {
	cfg := &Config{
		ListenAddr:   envStr("LISTEN_ADDR", ":8120"),
		ServiceToken: envStr("SERVICE_TOKEN", ""),
	}

	if raw := os.Getenv("SEARCH_BACKENDS"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg.Backends)
	}

	if cfg.Backends == nil {
		cfg.Backends = buildBackendFromEnv()
	}

	return cfg
}

func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddr:   ":8120",
		ServiceToken: envStr("SERVICE_TOKEN", ""),
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func buildBackendFromEnv() []BackendDef {
	host := envStr("ES_HOST", "")
	if host == "" {
		return nil
	}

	hosts := strings.Split(host, ",")
	for i := range hosts {
		hosts[i] = strings.TrimSpace(hosts[i])
	}

	return []BackendDef{
		{
			Type:     "elasticsearch",
			Hosts:    hosts,
			Index:    envStr("ES_INDEX", "phabricator"),
			Version:  envInt("ES_VERSION", 5),
			Timeout:  envInt("ES_TIMEOUT", 15),
			Protocol: envStr("ES_PROTOCOL", "http"),
			Roles:    []string{"read", "write"},
		},
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}
