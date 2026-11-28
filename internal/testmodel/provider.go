// Package testmodel loads opt-in real-model configuration for acceptance tests.
package testmodel

import (
	"database/sql"
	"encoding/json"
	"ghostreaper/internal/failover"
	"ghostreaper/internal/rings"
	"ghostreaper/internal/secrets"
	"github.com/Autumn-27/norma/llm"
	"net/url"
	"os"
	"testing"
)

// Config reads the opt-in configuration without persisting credentials.
func Config(t *testing.T) failover.ModelConfig {
	t.Helper()
	file := os.Getenv("GHOSTREAPER_REVIEW_MODEL_FILE")
	var b []byte
	var err error
	if memory := os.Getenv("GHOSTREAPER_REVIEW_MODEL_JSON"); memory != "" {
		b = []byte(memory)
	} else if file != "" {
		b, err = os.ReadFile(file)
	} else if source := os.Getenv("GHOSTREAPER_REVIEW_SETTINGS_DB"); source != "" {
		// Read-only access, no migrations or plaintext credential files.
		db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String())
		if e != nil {
			t.Fatal("cannot open model settings read-only")
		}
		defer db.Close()
		var name, sealed string
		if db.QueryRow("SELECT value FROM settings WHERE key='model.active.primary'").Scan(&name) != nil || db.QueryRow("SELECT value FROM settings WHERE key=?", "model.library."+name).Scan(&sealed) != nil {
			t.Fatal("cannot read active model settings")
		}
		key, e := os.ReadFile(os.Getenv("GHOSTREAPER_REVIEW_KEY_FILE"))
		if e != nil {
			t.Fatal("cannot read model encryption key")
		}
		cipher, e := secrets.NewCipher(key)
		if e != nil {
			t.Fatal("cannot initialize model settings reader")
		}
		plain, e := cipher.Open(sealed)
		if e != nil {
			t.Fatal("cannot decrypt active model settings")
		}
		b = []byte(plain)
	} else {
		t.Skip("real-model configuration not supplied")
	}
	if err != nil {
		t.Fatal("cannot read local real-model configuration")
	}
	var cfg failover.ModelConfig
	if json.Unmarshal(b, &cfg) != nil || cfg.APIKey == "" {
		t.Fatal("invalid local real-model configuration")
	}
	return cfg
}

// Provider uses the production chain, including outgoing-message cleanup.
// Model settings live only in memory; task evidence stores never receive keys.
func Provider(t *testing.T) llm.Provider {
	t.Helper()
	cfg := Config(t)
	if cfg.Name == "" {
		cfg.Name = "acceptance"
	}
	store, err := rings.Open(":memory:")
	if err != nil {
		t.Fatal("cannot initialize in-memory model settings")
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = failover.SaveModelConfig(t.Context(), store, cfg, "acceptance"); err != nil {
		t.Fatal("cannot configure acceptance model")
	}
	if err = failover.SetActive(t.Context(), store, failover.RolePrimary, cfg.Name, "acceptance"); err != nil {
		t.Fatal("cannot activate acceptance model")
	}
	return failover.NewChain(store, failover.EnvConfig{})
}
