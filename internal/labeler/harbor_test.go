package labeler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient wires a Client against a fake Harbor handler with retries
// disabled between attempts.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "robot$labeler", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.retryDelay = 0
	return c
}

func checkAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, pass, ok := r.BasicAuth()
	if !ok || user != "robot$labeler" || pass != "secret" {
		t.Errorf("missing or wrong basic auth on %s %s", r.Method, r.URL)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func TestFindGlobalLabelDoesNotCreateMissingLabel(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2.0/labels" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		writeJSON(w, []map[string]any{})
	}))

	id, found, err := c.FindGlobalLabel(context.Background(), "running-prod")
	if err != nil {
		t.Fatalf("FindGlobalLabel: %v", err)
	}
	if found || id != 0 {
		t.Errorf("FindGlobalLabel = (%d, %t), want (0, false)", id, found)
	}
}

func TestEnsureGlobalLabelFindsExisting(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2.0/labels" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		if q := r.URL.Query(); q.Get("name") != "running-prod" || q.Get("scope") != "g" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		writeJSON(w, []map[string]any{{"id": 7, "name": "running-prod", "scope": "g"}})
	}))

	id, err := c.EnsureGlobalLabel(context.Background(), "running-prod")
	if err != nil {
		t.Fatalf("EnsureGlobalLabel: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
}

func TestEnsureGlobalLabelCreatesWhenMissing(t *testing.T) {
	created := false
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2.0/labels":
			if !created {
				writeJSON(w, []map[string]any{})
				return
			}
			writeJSON(w, []map[string]any{{"id": 9, "name": "running-prod", "scope": "g"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2.0/labels":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "running-prod" || body["scope"] != "g" {
				t.Errorf("unexpected create body: %v", body)
			}
			created = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))

	id, err := c.EnsureGlobalLabel(context.Background(), "running-prod")
	if err != nil {
		t.Fatalf("EnsureGlobalLabel: %v", err)
	}
	if id != 9 {
		t.Errorf("id = %d, want 9", id)
	}
	if !created {
		t.Error("label was never created")
	}
}

func TestListAllLabeledArtifactsPaginatesEveryListing(t *testing.T) {
	const lastDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		path := r.URL.EscapedPath()
		page := r.URL.Query().Get("page")

		switch {
		case path == "/api/v2.0/projects" && page == "1":
			projects := make([]map[string]any, 0, pageSize)
			for i := range pageSize {
				projects = append(projects, map[string]any{"name": fmt.Sprintf("project-%03d", i)})
			}
			writeJSON(w, projects)
		case path == "/api/v2.0/projects" && page == "2":
			writeJSON(w, []map[string]any{{"name": "team"}})
		case strings.HasPrefix(path, "/api/v2.0/projects/project-") && strings.HasSuffix(path, "/repositories"):
			writeJSON(w, []map[string]any{})
		case path == "/api/v2.0/projects/team/repositories" && page == "1":
			repos := make([]map[string]any, 0, pageSize)
			for i := range pageSize {
				repos = append(repos, map[string]any{"name": fmt.Sprintf("team/repo-%03d", i)})
			}
			writeJSON(w, repos)
		case path == "/api/v2.0/projects/team/repositories" && page == "2":
			writeJSON(w, []map[string]any{{"name": "team/target"}})
		case strings.HasPrefix(path, "/api/v2.0/projects/team/repositories/repo-") && strings.HasSuffix(path, "/artifacts"):
			writeJSON(w, []map[string]any{})
		case path == "/api/v2.0/projects/team/repositories/target/artifacts" && page == "1":
			artifacts := make([]map[string]any, 0, pageSize)
			for i := range pageSize {
				artifacts = append(artifacts, map[string]any{"digest": fmt.Sprintf("sha256:%064d", i)})
			}
			writeJSON(w, artifacts)
		case path == "/api/v2.0/projects/team/repositories/target/artifacts" && page == "2":
			writeJSON(w, []map[string]any{{"digest": lastDigest}})
		default:
			t.Errorf("unexpected request: %s %s?%s", r.Method, path, r.URL.RawQuery)
			http.NotFound(w, r)
		}
	}))

	artifacts, err := c.ListAllLabeledArtifacts(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListAllLabeledArtifacts: %v", err)
	}
	if len(artifacts) != pageSize+1 {
		t.Fatalf("got %d artifacts, want %d", len(artifacts), pageSize+1)
	}
	wantLast := ArtifactRef{Project: "team", Repository: "target", Digest: lastDigest}
	if artifacts[pageSize] != wantLast {
		t.Errorf("last artifact = %v, want %v", artifacts[pageSize], wantLast)
	}
}

func TestListAllLabeledArtifacts(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		switch r.URL.EscapedPath() {
		case "/api/v2.0/projects":
			writeJSON(w, []map[string]any{{"name": "team"}})
		case "/api/v2.0/projects/team/repositories":
			writeJSON(w, []map[string]any{
				{"name": "team/sub/app"}, // Harbor returns project-prefixed repo names
			})
		case "/api/v2.0/projects/team/repositories/sub%252Fapp/artifacts":
			// Nested repository path must be double-encoded.
			if q := r.URL.Query().Get("q"); q != "labels=(7)" {
				t.Errorf("q = %q, want labels=(7)", q)
			}
			writeJSON(w, []map[string]any{{"digest": digest}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
			http.NotFound(w, r)
		}
	}))

	artifacts, err := c.ListAllLabeledArtifacts(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListAllLabeledArtifacts: %v", err)
	}
	want := []ArtifactRef{{Project: "team", Repository: "sub/app", Digest: digest}}
	if len(artifacts) != 1 || artifacts[0] != want[0] {
		t.Errorf("got %v, want %v", artifacts, want)
	}
}

func TestListAllLabeledArtifactsPartialFailure(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v2.0/projects":
			writeJSON(w, []map[string]any{{"name": "broken"}, {"name": "ok"}})
		case "/api/v2.0/projects/broken/repositories":
			w.WriteHeader(http.StatusForbidden)
		case "/api/v2.0/projects/ok/repositories":
			writeJSON(w, []map[string]any{{"name": "ok/app"}})
		case "/api/v2.0/projects/ok/repositories/app/artifacts":
			writeJSON(w, []map[string]any{{"digest": digest}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
			http.NotFound(w, r)
		}
	}))

	artifacts, err := c.ListAllLabeledArtifacts(context.Background(), 7)
	if err == nil {
		t.Fatal("expected error for the broken project")
	}
	want := ArtifactRef{Project: "ok", Repository: "app", Digest: digest}
	if len(artifacts) != 1 || artifacts[0] != want {
		t.Errorf("got %v, want partial result %v", artifacts, want)
	}
}

func TestListAllLabeledArtifactsRecordsProxyCacheProjects(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v2.0/projects":
			writeJSON(w, []map[string]any{
				{"name": "docker-hub", "registry_id": 5},
				{"name": "owned", "registry_id": nil},
			})
		case "/api/v2.0/projects/docker-hub/repositories", "/api/v2.0/projects/owned/repositories":
			writeJSON(w, []map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
			http.NotFound(w, r)
		}
	}))

	if _, err := c.ListAllLabeledArtifacts(context.Background(), 7); err != nil {
		t.Fatalf("ListAllLabeledArtifacts: %v", err)
	}
	if !c.IsProxyCacheProject("docker-hub") {
		t.Error("docker-hub not detected as proxy cache")
	}
	if c.IsProxyCacheProject("owned") {
		t.Error("owned project incorrectly detected as proxy cache")
	}
}

func TestAddLabel(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref := ArtifactRef{Project: "backend", Repository: "api", Digest: digest}

	t.Run("posts label id", func(t *testing.T) {
		var gotBody map[string]any
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			checkAuth(t, r)
			wantPath := "/api/v2.0/projects/backend/repositories/api/artifacts/" + digest + "/labels"
			if r.Method != http.MethodPost || r.URL.EscapedPath() != wantPath {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
			}
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
		}))
		if err := c.AddLabel(context.Background(), ref, 7); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
		if gotBody["id"] != float64(7) {
			t.Errorf("body = %v, want id 7", gotBody)
		}
	})

	t.Run("conflict means already labeled, not an error", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		if err := c.AddLabel(context.Background(), ref, 7); err != nil {
			t.Fatalf("AddLabel on 409: %v", err)
		}
	})
	t.Run("not found identifies a missing artifact", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		err := c.AddLabel(context.Background(), ref, 7)
		if !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("AddLabel on 404 = %v, want ErrArtifactNotFound", err)
		}
	})
}

func TestRemoveLabel(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref := ArtifactRef{Project: "backend", Repository: "api", Digest: digest}

	t.Run("deletes label", func(t *testing.T) {
		called := false
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			checkAuth(t, r)
			called = true
			wantPath := "/api/v2.0/projects/backend/repositories/api/artifacts/" + digest + "/labels/7"
			if r.Method != http.MethodDelete || r.URL.EscapedPath() != wantPath {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusOK)
		}))
		if err := c.RemoveLabel(context.Background(), ref, 7); err != nil {
			t.Fatalf("RemoveLabel: %v", err)
		}
		if !called {
			t.Error("no request made")
		}
	})

	t.Run("not found means already gone, not an error", func(t *testing.T) {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		if err := c.RemoveLabel(context.Background(), ref, 7); err != nil {
			t.Fatalf("RemoveLabel on 404: %v", err)
		}
	})
}

func TestRetriesOnServerError(t *testing.T) {
	attempts := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, []map[string]any{{"id": 7, "name": "running-prod", "scope": "g"}})
	}))

	id, err := c.EnsureGlobalLabel(context.Background(), "running-prod")
	if err != nil {
		t.Fatalf("EnsureGlobalLabel after retry: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	attempts := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusForbidden)
	}))

	if _, err := c.EnsureGlobalLabel(context.Background(), "running-prod"); err == nil {
		t.Fatal("expected error on 403")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts)
	}
}
