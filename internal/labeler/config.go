package labeler

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Config holds everything harbor-labeler needs, sourced from the environment.
type Config struct {
	HarborURL   string
	Username    string
	Password    string
	ClusterName string
	// RegistryHost is derived from HarborURL; only images pulled from this
	// host are considered.
	RegistryHost string
	// PodPhases restricts image discovery to pods in these phases; empty
	// means every pod object counts, regardless of phase.
	PodPhases []corev1.PodPhase
}

// LoadConfig reads and validates the required environment variables.
func LoadConfig() (Config, error) {
	cfg := Config{
		HarborURL:   os.Getenv("HARBOR_URL"),
		Username:    os.Getenv("HARBOR_USERNAME"),
		Password:    os.Getenv("HARBOR_PASSWORD"),
		ClusterName: os.Getenv("CLUSTER_NAME"),
	}
	for name, value := range map[string]string{
		"HARBOR_URL":      cfg.HarborURL,
		"HARBOR_USERNAME": cfg.Username,
		"HARBOR_PASSWORD": cfg.Password,
		"CLUSTER_NAME":    cfg.ClusterName,
	} {
		if value == "" {
			return Config{}, fmt.Errorf("required environment variable %s is not set", name)
		}
	}

	u, err := url.Parse(cfg.HarborURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Config{}, fmt.Errorf("HARBOR_URL %q is not a valid URL", cfg.HarborURL)
	}
	cfg.RegistryHost = u.Host

	if raw := os.Getenv("POD_PHASES"); raw != "" {
		cfg.PodPhases, err = parsePodPhases(raw)
		if err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// parsePodPhases parses the optional comma-separated POD_PHASES value into
// canonical pod phases, case-insensitively.
func parsePodPhases(raw string) ([]corev1.PodPhase, error) {
	known := map[string]corev1.PodPhase{
		"pending":   corev1.PodPending,
		"running":   corev1.PodRunning,
		"succeeded": corev1.PodSucceeded,
		"failed":    corev1.PodFailed,
		"unknown":   corev1.PodUnknown,
	}
	items := strings.Split(raw, ",")
	phases := make([]corev1.PodPhase, 0, len(items))
	for _, item := range items {
		phase, ok := known[strings.ToLower(strings.TrimSpace(item))]
		if !ok {
			return nil, fmt.Errorf("POD_PHASES: invalid pod phase %q", strings.TrimSpace(item))
		}
		phases = append(phases, phase)
	}
	return phases, nil
}
