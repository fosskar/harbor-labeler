package labeler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxAttempts = 3
	pageSize    = 100
)

// Client is a minimal Harbor v2 API client covering exactly the surface
// harbor-labeler needs: global labels, project/repository/artifact listing,
// and artifact label attach/detach.
type Client struct {
	baseURL    string // e.g. https://harbor.example.com/api/v2.0
	username   string
	password   string
	http       *http.Client
	retryDelay time.Duration
}

// ArtifactRef identifies one artifact in Harbor by digest.
type ArtifactRef struct {
	Project    string
	Repository string // without project prefix, may contain slashes
	Digest     string
}

func (a ArtifactRef) String() string {
	return fmt.Sprintf("%s/%s@%s", a.Project, a.Repository, a.Digest)
}

// NewClient creates a Harbor client for the given base URL using basic auth.
func NewClient(harborURL, username, password string) (*Client, error) {
	u, err := url.Parse(harborURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Harbor URL %q", harborURL)
	}
	return &Client{
		baseURL:    strings.TrimSuffix(harborURL, "/") + "/api/v2.0",
		username:   username,
		password:   password,
		http:       &http.Client{Timeout: 30 * time.Second},
		retryDelay: 2 * time.Second,
	}, nil
}

// do performs one authenticated request with retries on transport errors and
// 5xx responses. It returns the final status code and response body.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte) (int, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			case <-time.After(c.retryDelay):
			}
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return 0, nil, err
		}
		req.SetBasicAuth(c.username, c.password)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s %s: %s", method, rawURL, resp.Status)
			continue
		}
		return resp.StatusCode, respBody, nil
	}
	return 0, nil, fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) getJSON(ctx context.Context, rawURL string, out any) error {
	status, body, err := c.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: status %d: %s", rawURL, status, body)
	}
	return json.Unmarshal(body, out)
}

type harborLabel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// EnsureGlobalLabel returns the ID of the global label with the given name,
// creating it if it does not exist yet.
func (c *Client) EnsureGlobalLabel(ctx context.Context, name string) (int64, error) {
	find := func() (int64, bool, error) {
		var labels []harborLabel
		u := fmt.Sprintf("%s/labels?name=%s&scope=g", c.baseURL, url.QueryEscape(name))
		if err := c.getJSON(ctx, u, &labels); err != nil {
			return 0, false, err
		}
		for _, l := range labels {
			if l.Name == name {
				return l.ID, true, nil
			}
		}
		return 0, false, nil
	}

	id, found, err := find()
	if err != nil {
		return 0, err
	}
	if found {
		return id, nil
	}

	body, _ := json.Marshal(map[string]string{
		"name":        name,
		"scope":       "g",
		"description": "managed by harbor-labeler; attached to images running in this cluster",
	})
	status, respBody, err := c.do(ctx, http.MethodPost, c.baseURL+"/labels", body)
	if err != nil {
		return 0, err
	}
	// 409: another run created it concurrently — fall through to lookup.
	if status != http.StatusCreated && status != http.StatusConflict {
		return 0, fmt.Errorf("creating label %q: status %d: %s", name, status, respBody)
	}

	id, found, err = find()
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("label %q not found after creation", name)
	}
	return id, nil
}

// ListProjects returns the names of all projects visible to the account.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	var names []string
	for page := 1; ; page++ {
		var projects []struct {
			Name string `json:"name"`
		}
		u := fmt.Sprintf("%s/projects?page=%d&page_size=%d", c.baseURL, page, pageSize)
		if err := c.getJSON(ctx, u, &projects); err != nil {
			return nil, err
		}
		for _, p := range projects {
			names = append(names, p.Name)
		}
		if len(projects) < pageSize {
			return names, nil
		}
	}
}

// ListLabeledArtifacts returns all artifacts in the project that currently
// carry the given label.
func (c *Client) ListLabeledArtifacts(ctx context.Context, project string, labelID int64) ([]ArtifactRef, error) {
	repos, err := c.listRepositories(ctx, project)
	if err != nil {
		return nil, err
	}

	var refs []ArtifactRef
	for _, repo := range repos {
		for page := 1; ; page++ {
			var artifacts []struct {
				Digest string `json:"digest"`
			}
			u := fmt.Sprintf("%s/projects/%s/repositories/%s/artifacts?q=%s&page=%d&page_size=%d",
				c.baseURL, url.PathEscape(project), encodeRepository(repo),
				url.QueryEscape(fmt.Sprintf("labels=(%d)", labelID)), page, pageSize)
			if err := c.getJSON(ctx, u, &artifacts); err != nil {
				return nil, err
			}
			for _, a := range artifacts {
				refs = append(refs, ArtifactRef{Project: project, Repository: repo, Digest: a.Digest})
			}
			if len(artifacts) < pageSize {
				break
			}
		}
	}
	return refs, nil
}

// listRepositories returns repository names in the project, without the
// project name prefix Harbor includes in listings.
func (c *Client) listRepositories(ctx context.Context, project string) ([]string, error) {
	var names []string
	for page := 1; ; page++ {
		var repos []struct {
			Name string `json:"name"`
		}
		u := fmt.Sprintf("%s/projects/%s/repositories?page=%d&page_size=%d",
			c.baseURL, url.PathEscape(project), page, pageSize)
		if err := c.getJSON(ctx, u, &repos); err != nil {
			return nil, err
		}
		for _, r := range repos {
			names = append(names, strings.TrimPrefix(r.Name, project+"/"))
		}
		if len(repos) < pageSize {
			return names, nil
		}
	}
}

// AddLabel attaches the label to the artifact. An already-attached label
// (409) is not an error.
func (c *Client) AddLabel(ctx context.Context, ref ArtifactRef, labelID int64) error {
	body, _ := json.Marshal(map[string]int64{"id": labelID})
	status, respBody, err := c.do(ctx, http.MethodPost, c.artifactURL(ref)+"/labels", body)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusConflict:
		return nil
	}
	return fmt.Errorf("adding label to %s: status %d: %s", ref, status, respBody)
}

// RemoveLabel detaches the label from the artifact. A label or artifact that
// is already gone (404) is not an error.
func (c *Client) RemoveLabel(ctx context.Context, ref ArtifactRef, labelID int64) error {
	u := fmt.Sprintf("%s/labels/%d", c.artifactURL(ref), labelID)
	status, respBody, err := c.do(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("removing label from %s: status %d: %s", ref, status, respBody)
}

func (c *Client) artifactURL(ref ArtifactRef) string {
	return fmt.Sprintf("%s/projects/%s/repositories/%s/artifacts/%s",
		c.baseURL, url.PathEscape(ref.Project), encodeRepository(ref.Repository), url.PathEscape(ref.Digest))
}

// encodeRepository double-encodes slashes in nested repository names as the
// Harbor API requires ("sub/app" -> "sub%252Fapp").
func encodeRepository(repo string) string {
	return url.PathEscape(url.PathEscape(repo))
}
