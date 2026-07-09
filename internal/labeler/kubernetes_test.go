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
