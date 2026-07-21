//go:build e2e

package e2e

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// e2eConfig is the environment contract e2e/run.sh provisions for the suite.
type e2eConfig struct {
	HarborURL        string
	HarborUsername   string
	HarborPassword   string
	ClusterName      string
	LabelerBin       string
	ImageA           string
	ImageB           string
	ImagePromoted    string
	Cronjob          string
	CronjobNamespace string
	TLS              e2eTLSConfig
}

// e2eTLSConfig covers the "chart run over TLS" stage: one suspended chart
// release per customCAs source variant, all reaching Harbor through the
// nginx TLS proxy.
type e2eTLSConfig struct {
	Image       string
	Cronjob     string
	ClusterName string
	Variants    []string
}

// contractVars is the full env contract, in the order run.sh exports it.
var contractVars = []string{
	"HARBOR_URL",
	"HARBOR_USERNAME",
	"HARBOR_PASSWORD",
	"CLUSTER_NAME",
	"LABELER_BIN",
	"E2E_IMAGE_A",
	"E2E_IMAGE_B",
	"E2E_IMAGE_PROMOTED",
	"E2E_IMAGE_TLS",
	"E2E_CRONJOB",
	"E2E_CRONJOB_NAMESPACE",
	"E2E_TLS_CRONJOB",
	"E2E_TLS_CLUSTER_NAME",
	"E2E_TLS_VARIANTS",
}

// loadE2EConfig reads and validates the env contract all-or-nothing: a fully
// empty contract skips the suite (bare `go test -tags e2e` stays green), a
// partially set one fails it, so a renamed or dropped export in run.sh can
// never silently skip a stage.
func loadE2EConfig(t *testing.T) e2eConfig {
	t.Helper()

	values := make(map[string]string, len(contractVars))
	var present, missing []string
	for _, name := range contractVars {
		values[name] = os.Getenv(name)
		if values[name] == "" {
			missing = append(missing, name)
		} else {
			present = append(present, name)
		}
	}
	if len(present) == 0 {
		t.Skip("e2e env contract not set; infrastructure not provisioned (see e2e/run.sh)")
	}
	if len(missing) > 0 {
		t.Fatalf("partial e2e env contract: set: %s; missing: %s (see e2e/run.sh)",
			strings.Join(present, ", "), strings.Join(missing, ", "))
	}

	if u, err := url.Parse(values["HARBOR_URL"]); err != nil || u.Scheme == "" || u.Host == "" {
		t.Fatalf("HARBOR_URL %q is not a valid URL", values["HARBOR_URL"])
	}
	if info, err := os.Stat(values["LABELER_BIN"]); err != nil {
		t.Fatalf("LABELER_BIN %q: %v", values["LABELER_BIN"], err)
	} else if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("LABELER_BIN %q is not an executable file", values["LABELER_BIN"])
	}
	variants := strings.Fields(values["E2E_TLS_VARIANTS"])
	if len(variants) == 0 {
		t.Fatalf("E2E_TLS_VARIANTS %q contains no variants", values["E2E_TLS_VARIANTS"])
	}

	return e2eConfig{
		HarborURL:        strings.TrimSuffix(values["HARBOR_URL"], "/"),
		HarborUsername:   values["HARBOR_USERNAME"],
		HarborPassword:   values["HARBOR_PASSWORD"],
		ClusterName:      values["CLUSTER_NAME"],
		LabelerBin:       values["LABELER_BIN"],
		ImageA:           values["E2E_IMAGE_A"],
		ImageB:           values["E2E_IMAGE_B"],
		ImagePromoted:    values["E2E_IMAGE_PROMOTED"],
		Cronjob:          values["E2E_CRONJOB"],
		CronjobNamespace: values["E2E_CRONJOB_NAMESPACE"],
		TLS: e2eTLSConfig{
			Image:       values["E2E_IMAGE_TLS"],
			Cronjob:     values["E2E_TLS_CRONJOB"],
			ClusterName: values["E2E_TLS_CLUSTER_NAME"],
			Variants:    variants,
		},
	}
}
