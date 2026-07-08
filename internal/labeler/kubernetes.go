package labeler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// GetRunningImages lists all pods in the cluster and returns the sorted,
// unique artifacts whose container images are hosted on registryHost. A
// non-empty phases list restricts discovery to pods in those phases; empty
// means every pod object counts.
func GetRunningImages(ctx context.Context, client kubernetes.Interface, registryHost string, phases []corev1.PodPhase) ([]ArtifactRef, error) {
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	var phaseSet map[corev1.PodPhase]struct{}
	if len(phases) > 0 {
		phaseSet = make(map[corev1.PodPhase]struct{}, len(phases))
		for _, phase := range phases {
			phaseSet[phase] = struct{}{}
		}
	}

	refs := make(map[ArtifactRef]struct{})
	for _, pod := range pods.Items {
		if phaseSet != nil {
			if _, ok := phaseSet[pod.Status.Phase]; !ok {
				continue
			}
		}
		collectImageRefs(refs, pod.Status.ContainerStatuses, registryHost)
		collectImageRefs(refs, pod.Status.InitContainerStatuses, registryHost)
	}

	if len(refs) == 0 {
		return nil, nil
	}
	images := make([]ArtifactRef, 0, len(refs))
	for ref := range refs {
		images = append(images, ref)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].String() < images[j].String() })
	return images, nil
}

// collectImageRefs extracts artifacts hosted on registryHost from container
// statuses into refs. Refs without a project/repository structure cannot
// exist in Harbor and are skipped with a log line.
func collectImageRefs(refs map[ArtifactRef]struct{}, statuses []corev1.ContainerStatus, registryHost string) {
	for _, st := range statuses {
		imageID := st.ImageID
		if imageID == "" {
			continue
		}
		// Strip runtime scheme prefixes like "docker-pullable://".
		if idx := strings.Index(imageID, "://"); idx != -1 {
			imageID = imageID[idx+len("://"):]
		}
		host, rest, ok := strings.Cut(imageID, "/")
		if !ok || host != registryHost {
			continue
		}
		ref, ok := parseImageRef(rest)
		if !ok {
			log.Printf("skipping image ref %q: no project/repository structure", rest)
			continue
		}
		refs[ref] = struct{}{}
	}
}

// parseImageRef splits "project/repo[/sub]@sha256:digest" into an
// ArtifactRef. Refs without a project/repository structure are rejected.
func parseImageRef(ref string) (ArtifactRef, bool) {
	path, digest, ok := strings.Cut(ref, "@")
	if !ok || digest == "" {
		return ArtifactRef{}, false
	}
	project, repo, ok := strings.Cut(path, "/")
	if !ok || project == "" || repo == "" {
		return ArtifactRef{}, false
	}
	return ArtifactRef{Project: project, Repository: repo, Digest: digest}, true
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
