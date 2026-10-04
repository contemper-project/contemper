package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// client is the small part of the GitHub REST API the report needs.
type client struct {
	api, repo, token string
	http             *http.Client
}

func newClient(api, repo, token string) *client {
	return &client{
		api:   strings.TrimSuffix(api, "/"),
		repo:  repo,
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

type issue struct {
	Number      int       `json:"number"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PullRequest *struct{} `json:"pull_request"`
}

type comment struct {
	Body string `json:"body"`
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func (c *client) do(method, url string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, url, err)
		}
	}
	return resp.Header, nil
}

// getAll fetches every page of a list endpoint into out, which must be a
// pointer to a slice.
func getAll[T any](c *client, path string) ([]T, error) {
	var all []T
	url := c.api + "/repos/" + c.repo + path
	for url != "" {
		var page []T
		h, err := c.do(http.MethodGet, url, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		url = ""
		if m := nextLink.FindStringSubmatch(h.Get("Link")); m != nil {
			// Only follow links that stay on the API we were pointed at.
			if !strings.HasPrefix(m[1], c.api+"/") {
				return nil, fmt.Errorf("pagination link %q leaves %s", m[1], c.api)
			}
			url = m[1]
		}
	}
	return all, nil
}

func (c *client) listIssues(label string) ([]issue, error) {
	all, err := getAll[issue](c, "/issues?state=all&per_page=100&labels="+queryEscape(label))
	if err != nil {
		return nil, err
	}
	// The issues endpoint also returns pull requests.
	out := all[:0]
	for _, i := range all {
		if i.PullRequest == nil {
			out = append(out, i)
		}
	}
	return out, nil
}

func (c *client) listComments(number int) ([]comment, error) {
	return getAll[comment](c, fmt.Sprintf("/issues/%d/comments?per_page=100", number))
}

func (c *client) createIssue(title, body string, labels []string) (issue, error) {
	var i issue
	_, err := c.do(http.MethodPost, c.api+"/repos/"+c.repo+"/issues",
		map[string]any{"title": title, "body": body, "labels": labels}, &i)
	return i, err
}

func (c *client) addComment(number int, body string) error {
	_, err := c.do(http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.api, c.repo, number),
		map[string]string{"body": body}, nil)
	return err
}

func (c *client) closeIssue(number int) error {
	_, err := c.do(http.MethodPatch, fmt.Sprintf("%s/repos/%s/issues/%d", c.api, c.repo, number),
		map[string]string{"state": "closed", "state_reason": "completed"}, nil)
	return err
}

func queryEscape(s string) string {
	return strings.NewReplacer(" ", "%20", ":", "%3A").Replace(s)
}
