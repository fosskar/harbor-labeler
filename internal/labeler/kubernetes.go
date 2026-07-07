package labeler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetRunningImages lists all pods in the cluster and returns the sorted,
// unique digest references ("project/repo@sha256:...") of container images
// hosted on registryHost.
func GetRunningImages(ctx context.Context, client kubernetes.Interface, registryHost string) ([]string, error) {
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	refs := make(map[string]struct{})
	for _, pod := range pods.Items {
		collectImageRefs(refs, pod.Status.ContainerStatuses, registryHost)
		collectImageRefs(refs, pod.Status.InitContainerStatuses, registryHost)
	}

	if len(refs) == 0 {
		return nil, nil
	}
	images := make([]string, 0, len(refs))
	for ref := range refs {
		images = append(images, ref)
	}
	sort.Strings(images)
	return images, nil
}

// collectImageRefs extracts digest references hosted on registryHost from
// container statuses into refs.
func collectImageRefs(refs map[string]struct{}, statuses []corev1.ContainerStatus, registryHost string) {
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
		refs[rest] = struct{}{}
	}
}
