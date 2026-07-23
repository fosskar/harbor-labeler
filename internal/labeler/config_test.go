package labeler

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

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

	t.Run("dry run accepts true case-insensitively", func(t *testing.T) {
		setAll(t)
		t.Setenv("DRY_RUN", "TRUE")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if !cfg.DryRun {
			t.Error("DryRun = false, want true")
		}
	})

	t.Run("dry run rejects invalid value", func(t *testing.T) {
		setAll(t)
		t.Setenv("DRY_RUN", "1")
		_, err := LoadConfig()
		if err == nil {
			t.Fatal("expected error for invalid DRY_RUN")
		}
		if !strings.Contains(err.Error(), "DRY_RUN") {
			t.Errorf("error %q does not mention DRY_RUN", err)
		}
	})

	t.Run("pod phases unset yields nil", func(t *testing.T) {
		setAll(t)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.PodPhases != nil {
			t.Errorf("PodPhases = %v, want nil", cfg.PodPhases)
		}
	})

	t.Run("pod phases single lowercase name canonicalized", func(t *testing.T) {
		setAll(t)
		t.Setenv("POD_PHASES", "running")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		want := []corev1.PodPhase{corev1.PodRunning}
		if !reflect.DeepEqual(cfg.PodPhases, want) {
			t.Errorf("PodPhases = %v, want %v", cfg.PodPhases, want)
		}
	})

	t.Run("pod phases list trims whitespace and preserves order", func(t *testing.T) {
		setAll(t)
		t.Setenv("POD_PHASES", " Running, succeeded ")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		want := []corev1.PodPhase{corev1.PodRunning, corev1.PodSucceeded}
		if !reflect.DeepEqual(cfg.PodPhases, want) {
			t.Errorf("PodPhases = %v, want %v", cfg.PodPhases, want)
		}
	})

	t.Run("pod phases unknown name fails", func(t *testing.T) {
		setAll(t)
		t.Setenv("POD_PHASES", "Sleeping")
		_, err := LoadConfig()
		if err == nil {
			t.Fatal("expected error for unknown pod phase")
		}
		if !strings.Contains(err.Error(), "POD_PHASES") {
			t.Errorf("error %q does not mention POD_PHASES", err)
		}
	})

	t.Run("pod phases empty item fails", func(t *testing.T) {
		setAll(t)
		t.Setenv("POD_PHASES", "Running,,Failed")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for empty pod phase item")
		}
	})
}
