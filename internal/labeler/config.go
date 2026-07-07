package labeler

import (
	"fmt"
	"net/url"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
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
	return cfg, nil
}

// NewKubeClient builds a clientset from the in-cluster service account when
// running inside Kubernetes, falling back to the standard kubeconfig
// resolution (KUBECONFIG, ~/.kube/config) otherwise.
func NewKubeClient() (kubernetes.Interface, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("building Kubernetes client config: %w", err)
		}
	}
	return kubernetes.NewForConfig(restCfg)
}
