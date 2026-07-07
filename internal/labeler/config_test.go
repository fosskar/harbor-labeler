package labeler

import "testing"

func TestLoadConfig(t *testing.T) {
	setAll := func(t *testing.T) {
		t.Setenv("HARBOR_URL", "https://harbor.example.com")
		t.Setenv("HARBOR_USERNAME", "robot$labeler")
		t.Setenv("HARBOR_PASSWORD", "secret")
		t.Setenv("CLUSTER_NAME", "prod")
	}

	t.Run("derives registry host from harbor url", func(t *testing.T) {
		setAll(t)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.RegistryHost != "harbor.example.com" {
			t.Errorf("RegistryHost = %q, want harbor.example.com", cfg.RegistryHost)
		}
	})

	t.Run("registry host keeps explicit port", func(t *testing.T) {
		setAll(t)
		t.Setenv("HARBOR_URL", "https://harbor.example.com:8443")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.RegistryHost != "harbor.example.com:8443" {
			t.Errorf("RegistryHost = %q, want harbor.example.com:8443", cfg.RegistryHost)
		}
	})

	t.Run("missing required variable fails", func(t *testing.T) {
		setAll(t)
		t.Setenv("CLUSTER_NAME", "")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for empty CLUSTER_NAME")
		}
	})

	t.Run("invalid url fails", func(t *testing.T) {
		setAll(t)
		t.Setenv("HARBOR_URL", "not-a-url")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for invalid HARBOR_URL")
		}
	})
}
