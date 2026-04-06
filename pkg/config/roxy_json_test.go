package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRoxyJSON_ParsesPortField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roxy.json")

	content := `{
	  "services": {
	    "api": {
	      "cmd": "npm run dev",
	      "port": 4000,
	      "tls": true,
	      "listen-port": 9000
	    }
	  }
	}`

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write roxy.json: %v", err)
	}

	cfg, err := LoadRoxyJSON(dir)
	if err != nil {
		t.Fatalf("LoadRoxyJSON returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}

	svc, ok := cfg.Services["api"]
	if !ok {
		t.Fatal("expected service 'api' to exist")
	}

	if svc.Port != 4000 {
		t.Fatalf("Port = %d, want 4000", svc.Port)
	}
	if !svc.TLS {
		t.Fatal("expected tls=true")
	}
	if svc.ListenPort != 9000 {
		t.Fatalf("ListenPort = %d, want 9000", svc.ListenPort)
	}
}

func TestLoadRoxyJSON_RejectsPostField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roxy.json")

	content := `{
	  "services": {
	    "api": {
	      "cmd": "npm run dev",
	      "post": 4000
	    }
	  }
	}`

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write roxy.json: %v", err)
	}

	_, err := LoadRoxyJSON(dir)
	if err == nil {
		t.Fatal("expected unknown field error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field \"post\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRoxyJSON_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roxy.json")

	content := `{
	  "services": {
	    "api": {
	      "cmd": "npm run dev",
	      "unknown": true
	    }
	  }
	}`

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write roxy.json: %v", err)
	}

	_, err := LoadRoxyJSON(dir)
	if err == nil {
		t.Fatal("expected unknown field error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}
}
