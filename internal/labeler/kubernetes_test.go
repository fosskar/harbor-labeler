package labeler

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(name, ns string, statuses, initStatuses []corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: corev1.PodStatus{
			ContainerStatuses:     statuses,
			InitContainerStatuses: initStatuses,
		},
	}
}

func status(imageID string) corev1.ContainerStatus {
	return corev1.ContainerStatus{ImageID: imageID}
}

// podInPhase is pod with status.phase set, for phase-filter cases.
func podInPhase(name, ns string, phase corev1.PodPhase, statuses, initStatuses []corev1.ContainerStatus) *corev1.Pod {
	p := pod(name, ns, statuses, initStatuses)
	p.Status.Phase = phase
	return p
}

// ctr pairs one container's spec-declared image with the kubelet-attested
// imageID under a shared container name, so a pod's spec and status can be
// made to disagree by design (the containerd dedup case).
type ctr struct {
	name      string
	specImage string
	imageID   string
}

// containersToSpecStatus splits container descriptions into the parallel
// spec.Containers and status.ContainerStatuses slices, paired by name.
func containersToSpecStatus(cs []ctr) ([]corev1.Container, []corev1.ContainerStatus) {
	specs := make([]corev1.Container, 0, len(cs))
	statuses := make([]corev1.ContainerStatus, 0, len(cs))
	for _, c := range cs {
		specs = append(specs, corev1.Container{Name: c.name, Image: c.specImage})
		statuses = append(statuses, corev1.ContainerStatus{Name: c.name, ImageID: c.imageID})
	}
	return specs, statuses
}

// specPod builds a pod whose spec containers and container statuses pair by
// name, for both the regular and init container groups.
func specPod(name, ns string, containers, initContainers []ctr) *corev1.Pod {
	specs, statuses := containersToSpecStatus(containers)
	initSpecs, initStatuses := containersToSpecStatus(initContainers)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			Containers:     specs,
			InitContainers: initSpecs,
		},
		Status: corev1.PodStatus{
			ContainerStatuses:     statuses,
			InitContainerStatuses: initStatuses,
		},
	}
}

func TestRunningImages(t *testing.T) {
	const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	tests := []struct {
		name   string
		pods   []*corev1.Pod
		phases []corev1.PodPhase
		want   []ArtifactRef
	}{
		{
			name: "matching registry returns artifact without host",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "foreign registry filtered out",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("docker.io/library/nginx@" + digestA),
					status("harbor.example.com/backend/api@" + digestB),
				}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestB}},
		},
		{
			name: "init containers included",
			pods: []*corev1.Pod{
				pod("a", "default", nil, []corev1.ContainerStatus{
					status("harbor.example.com/backend/migrate@" + digestA),
				}),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "migrate", Digest: digestA}},
		},
		{
			name: "duplicates across pods deduplicated",
			pods: []*corev1.Pod{
				pod("a", "ns1", []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
				pod("b", "ns2", []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "docker-pullable prefix stripped",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("docker-pullable://harbor.example.com/backend/api@" + digestA),
				}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "empty imageID skipped",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{status("")}, nil),
			},
			want: nil,
		},
		{
			name: "ref without project/repository structure skipped",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("harbor.example.com/no-project-segment@" + digestA),
					status("harbor.example.com/backend/api@" + digestB),
				}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestB}},
		},
		{
			name: "ref without digest skipped",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("harbor.example.com/backend/api:v1"),
				}, nil),
			},
			want: nil,
		},
		{
			name: "nested repository path preserved",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("harbor.example.com/team/sub/app@" + digestA),
				}, nil),
			},
			want: []ArtifactRef{{Project: "team", Repository: "sub/app", Digest: digestA}},
		},
		{
			name: "results sorted",
			pods: []*corev1.Pod{
				pod("a", "default", []corev1.ContainerStatus{
					status("harbor.example.com/z/app@" + digestB),
					status("harbor.example.com/a/app@" + digestA),
				}, nil),
			},
			want: []ArtifactRef{
				{Project: "a", Repository: "app", Digest: digestA},
				{Project: "z", Repository: "app", Digest: digestB},
			},
		},
		{
			name: "nil phases keeps pods in every phase",
			pods: []*corev1.Pod{
				podInPhase("running", "default", corev1.PodRunning, []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
				podInPhase("succeeded", "default", corev1.PodSucceeded, []corev1.ContainerStatus{
					status("harbor.example.com/batch/job@" + digestB),
				}, nil),
			},
			want: []ArtifactRef{
				{Project: "backend", Repository: "api", Digest: digestA},
				{Project: "batch", Repository: "job", Digest: digestB},
			},
		},
		{
			name: "phase filter drops non-matching pod including init containers",
			pods: []*corev1.Pod{
				podInPhase("running", "default", corev1.PodRunning, []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
				podInPhase("succeeded", "default", corev1.PodSucceeded,
					[]corev1.ContainerStatus{
						status("harbor.example.com/batch/job@" + digestB),
					},
					[]corev1.ContainerStatus{
						status("harbor.example.com/batch/setup@" + digestB),
					}),
			},
			phases: []corev1.PodPhase{corev1.PodRunning},
			want:   []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "multi-phase filter includes all listed phases",
			pods: []*corev1.Pod{
				podInPhase("running", "default", corev1.PodRunning, []corev1.ContainerStatus{
					status("harbor.example.com/backend/api@" + digestA),
				}, nil),
				podInPhase("succeeded", "default", corev1.PodSucceeded, []corev1.ContainerStatus{
					status("harbor.example.com/batch/job@" + digestB),
				}, nil),
				// pending pod already pulled its init image; still excluded.
				podInPhase("pending", "default", corev1.PodPending, nil, []corev1.ContainerStatus{
					status("harbor.example.com/pending/setup@" + digestA),
				}),
			},
			phases: []corev1.PodPhase{corev1.PodRunning, corev1.PodSucceeded},
			want: []ArtifactRef{
				{Project: "backend", Repository: "api", Digest: digestA},
				{Project: "batch", Repository: "job", Digest: digestB},
			},
		},
		{
			name: "containerd dedup attributes one digest to both spec and imageID repos",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/mds-stg/app:v1",
					imageID:   "harbor.example.com/mds-qa/app@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{
				{Project: "mds-qa", Repository: "app", Digest: digestA},
				{Project: "mds-stg", Repository: "app", Digest: digestA},
			},
		},
		{
			name: "spec and imageID agreeing yields a single ref",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/backend/api:v1",
					imageID:   "harbor.example.com/backend/api@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "foreign spec image with harbor imageID keeps only the imageID ref",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "docker.io/library/app:v1",
					imageID:   "harbor.example.com/backend/api@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "harbor spec image with foreign imageID keeps the spec ref with the attested digest",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/mds-stg/app:v1",
					imageID:   "docker.io/library/app@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{{Project: "mds-stg", Repository: "app", Digest: digestA}},
		},
		{
			name: "digest-pinned spec image still takes its digest from the status",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/backend/api@" + digestB,
					imageID:   "harbor.example.com/backend/api@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "spec image without project structure is skipped while imageID ref survives",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/only-repo:v1",
					imageID:   "harbor.example.com/backend/api@" + digestA,
				}}, nil),
			},
			want: []ArtifactRef{{Project: "backend", Repository: "api", Digest: digestA}},
		},
		{
			name: "init container spec and status pair by name",
			pods: []*corev1.Pod{
				specPod("a", "default", nil, []ctr{{
					name:      "migrate",
					specImage: "harbor.example.com/mds-stg/migrate:v1",
					imageID:   "harbor.example.com/mds-qa/migrate@" + digestA,
				}}),
			},
			want: []ArtifactRef{
				{Project: "mds-qa", Repository: "migrate", Digest: digestA},
				{Project: "mds-stg", Repository: "migrate", Digest: digestA},
			},
		},
		{
			name: "empty imageID emits nothing even when the spec image is on harbor",
			pods: []*corev1.Pod{
				specPod("a", "default", []ctr{{
					name:      "app",
					specImage: "harbor.example.com/mds-stg/app:v1",
					imageID:   "",
				}}, nil),
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tt.pods))
			for _, p := range tt.pods {
				objs = append(objs, p)
			}
			client := fake.NewSimpleClientset(objs...)
			got, err := NewKubeDiscovery(client, "harbor.example.com", tt.phases).RunningImages(context.Background())
			if err != nil {
				t.Fatalf("RunningImages: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunningImagesRegistryHostWithPort covers spec-image parsing when the
// registry host carries a port: the port colon must not be mistaken for a
// tag, and a real tag after the last path separator must still be stripped.
func TestRunningImagesRegistryHostWithPort(t *testing.T) {
	const digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	tests := []struct {
		name      string
		specImage string
		want      []ArtifactRef
	}{
		{
			name:      "port colon is not treated as a tag when the spec image has none",
			specImage: "harbor.example.com:30002/proj/app",
			want:      []ArtifactRef{{Project: "proj", Repository: "app", Digest: digest}},
		},
		{
			name:      "tag after the port is stripped from the spec image",
			specImage: "harbor.example.com:30002/proj/app:v2",
			want:      []ArtifactRef{{Project: "proj", Repository: "app", Digest: digest}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// imageID is a foreign mirror pull, so the only ref must come
			// from the spec path; the digest still comes from the status.
			p := specPod("a", "default", []ctr{{
				name:      "app",
				specImage: tt.specImage,
				imageID:   "docker.io/library/app@" + digest,
			}}, nil)
			client := fake.NewSimpleClientset(p)
			got, err := NewKubeDiscovery(client, "harbor.example.com:30002", nil).RunningImages(context.Background())
			if err != nil {
				t.Fatalf("RunningImages: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
