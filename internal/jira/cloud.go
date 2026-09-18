package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Cloud is a net/http-backed implementation of Client. Despite the name it
// speaks both dialects: Jira Cloud (REST API v3, ADF, accountId) under basic
// auth, and Jira Server/Data Center (REST API v2, plain-text, username/key)
// under bearer-PAT auth. The dialect is selected by authMode at construction.
type Cloud struct {
	baseURL    string
	authHeader string
	dc         bool // Data Center / Server dialect (REST v2) vs Cloud (v3)
	ver        string
	http       *http.Client
}

// NewCloud builds a client. authMode is "cloud" (basic email:token, REST v3) or
// "dc" (bearer PAT, REST v2).
func NewCloud(baseURL, authMode, email, apiToken, pat string, timeout time.Duration) *Cloud {
	dc := authMode == "dc"
	var authHeader string
	if dc {
		authHeader = "Bearer " + pat
	} else {
		creds := base64.StdEncoding.EncodeToString([]byte(email + ":" + apiToken))
		authHeader = "Basic " + creds
	}
	ver := "3"
	if dc {
		ver = "2"
	}
	return &Cloud{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: authHeader,
		dc:         dc,
		ver:        ver,
		http: &http.Client{
			Timeout: timeout,
			// Do not follow redirects: a 302 to an HTML login/XSRF page would
			// otherwise be returned as a 200 HTML body and fail JSON decoding with
			// a confusing error. Surfacing the redirect as a non-2xx is clearer.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// api builds a REST path for the active API version, e.g. "/rest/api/2/myself".
func (c *Cloud) api(suffix string) string {
	return "/rest/api/" + c.ver + suffix
}

// browseURL builds the human-facing URL for an issue key.
func (c *Cloud) browseURL(key string) string {
	return c.baseURL + "/browse/" + key
}

// BrowseURL implements Client.
func (c *Cloud) BrowseURL(key string) string { return c.browseURL(key) }

// userRef wraps a resolved user identifier in the dialect-appropriate object
// for an issue field: {"name": ...} for DC/Server, {"accountId": ...} for Cloud.
func (c *Cloud) userRef(id string) map[string]any {
	if c.dc {
		return map[string]any{"name": id}
	}
	return map[string]any{"accountId": id}
}

// do executes a request and returns the response body for 2xx, or an *APIError.
func (c *Cloud) do(ctx context.Context, op, method, path string, query url.Values, body any) ([]byte, error) {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s: marshaling request: %w", op, err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, full, reqBody)
	if err != nil {
		return nil, fmt.Errorf("%s: building request: %w", op, err)
	}
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")
	// Bypass Jira's XSRF check; Server/DC rejects mutating REST calls without it,
	// returning an HTML page instead of JSON. Harmless on Cloud and for GETs.
	req.Header.Set("X-Atlassian-Token", "no-check")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	// Cap the response body to avoid unbounded memory use.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: reading response: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter := 0
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			retryAfter, _ = strconv.Atoi(ra)
		}
		return nil, parseAPIError(op, resp.StatusCode, retryAfter, data)
	}
	return data, nil
}

// Myself implements Client.
func (c *Cloud) Myself(ctx context.Context) (*User, error) {
	data, err := c.do(ctx, "get current user", http.MethodGet, c.api("/myself"), nil, nil)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("decoding myself: %w", err)
	}
	return &u, nil
}

// SearchUsers implements Client. Cloud uses the `query` parameter; Data Center
// uses `username` and matches against username/display-name/email.
func (c *Cloud) SearchUsers(ctx context.Context, query string) ([]User, error) {
	q := url.Values{}
	if c.dc {
		q.Set("username", query)
	} else {
		q.Set("query", query)
	}
	q.Set("maxResults", "50")
	data, err := c.do(ctx, "search users", http.MethodGet, c.api("/user/search"), q, nil)
	if err != nil {
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("decoding user search: %w", err)
	}
	return users, nil
}

// SearchProjects implements Client. Cloud has a paginated /project/search with
// server-side filtering; Data Center returns the full /project array, which we
// filter client-side to honor the query.
func (c *Cloud) SearchProjects(ctx context.Context, query string) ([]Project, error) {
	if c.dc {
		data, err := c.do(ctx, "search projects", http.MethodGet, c.api("/project"), nil, nil)
		if err != nil {
			return nil, err
		}
		var all []Project
		if err := json.Unmarshal(data, &all); err != nil {
			return nil, fmt.Errorf("decoding project list: %w", err)
		}
		return filterProjects(all, query), nil
	}

	q := url.Values{}
	if query != "" {
		q.Set("query", query)
	}
	q.Set("maxResults", "50")
	data, err := c.do(ctx, "search projects", http.MethodGet, c.api("/project/search"), q, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Values []Project `json:"values"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decoding project search: %w", err)
	}
	return page.Values, nil
}

// filterProjects keeps projects whose key or name contains query (case
// insensitive). An empty query keeps everything.
func filterProjects(in []Project, query string) []Project {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return in
	}
	out := make([]Project, 0, len(in))
	for _, p := range in {
		if strings.Contains(strings.ToLower(p.Key), query) || strings.Contains(strings.ToLower(p.Name), query) {
			out = append(out, p)
		}
	}
	return out
}

// IssueTypes implements Client. On Data Center the issue types come straight
// from the project resource (the createmeta endpoint is unreliable across DC
// versions); on Cloud they come from the dedicated createmeta/issuetypes path.
func (c *Cloud) IssueTypes(ctx context.Context, projectKey string) ([]IssueType, error) {
	if c.dc {
		data, err := c.do(ctx, "list issue types", http.MethodGet,
			c.api("/project/"+url.PathEscape(projectKey)), nil, nil)
		if err != nil {
			return nil, err
		}
		var proj struct {
			IssueTypes []IssueType `json:"issueTypes"`
		}
		if err := json.Unmarshal(data, &proj); err != nil {
			return nil, fmt.Errorf("decoding project issue types: %w", err)
		}
		return proj.IssueTypes, nil
	}

	path := c.api("/issue/createmeta/" + url.PathEscape(projectKey) + "/issuetypes")
	data, err := c.do(ctx, "list issue types", http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	// Different Jira versions key the array as "issueTypes" or "values".
	var page struct {
		IssueTypes []IssueType `json:"issueTypes"`
		Values     []IssueType `json:"values"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decoding issue types: %w", err)
	}
	if len(page.IssueTypes) > 0 {
		return page.IssueTypes, nil
	}
	return page.Values, nil
}

// CreateMeta implements Client. The returned metadata is used for best-effort
// pre-validation; callers skip validation when this returns an error.
//
// On Jira Server/Data Center this deliberately FAILS OPEN: DC createmeta is
// unreliable (frequently restricted or disabled — verified broken on the target
// instance), and more importantly the reporter on-behalf-of flow must never be
// blocked by a metadata pre-check when the service account holds Modify
// Reporter. So DC always returns an error here, which callers treat as "skip
// pre-validation" and rely on Jira's own create-time validation instead.
func (c *Cloud) CreateMeta(ctx context.Context, projectKey, issueTypeID string) (*CreateMeta, error) {
	if c.dc {
		return nil, fmt.Errorf("createmeta pre-validation is skipped on Jira Server/Data Center")
	}

	path := c.api("/issue/createmeta/" + url.PathEscape(projectKey) +
		"/issuetypes/" + url.PathEscape(issueTypeID))
	data, err := c.do(ctx, "get create metadata", http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Fields []FieldMeta `json:"fields"`
		Values []FieldMeta `json:"values"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decoding create metadata: %w", err)
	}
	fields := page.Fields
	if len(fields) == 0 {
		fields = page.Values
	}
	meta := &CreateMeta{Fields: make(map[string]FieldMeta, len(fields))}
	for _, f := range fields {
		meta.Fields[f.FieldID] = f
	}
	return meta, nil
}

// CreateIssue implements Client.
func (c *Cloud) CreateIssue(ctx context.Context, in CreateIssueInput) (*CreatedIssue, error) {
	fields := map[string]any{}
	// Team-supplied extra fields (custom fields) are applied FIRST so the core
	// fields below always win. Combined with config-load validation that rejects
	// reserved keys, this prevents a mapping's `fields` from overriding routing
	// or identity (e.g. project/issuetype/reporter).
	for k, v := range in.ExtraFields {
		fields[k] = v
	}
	fields["project"] = map[string]any{"key": in.ProjectKey}
	fields["issuetype"] = map[string]any{"id": in.IssueTypeID}
	fields["summary"] = in.Summary
	if in.Description != "" {
		if c.dc {
			// DC/Server REST v2 expects a plain string (wiki markup).
			fields["description"] = in.Description
		} else {
			// Cloud REST v3 requires Atlassian Document Format.
			fields["description"] = TextToADF(in.Description)
		}
	}
	if in.ReporterID != "" {
		fields["reporter"] = c.userRef(in.ReporterID)
	}
	if in.AssigneeID != "" {
		fields["assignee"] = c.userRef(in.AssigneeID)
	}
	if in.Priority != "" {
		fields["priority"] = map[string]any{"name": in.Priority}
	}
	if len(in.Labels) > 0 {
		fields["labels"] = in.Labels
	}
	if len(in.Components) > 0 {
		comps := make([]any, 0, len(in.Components))
		for _, name := range in.Components {
			comps = append(comps, map[string]any{"name": name})
		}
		fields["components"] = comps
	}
	if in.DueDate != "" {
		fields["duedate"] = in.DueDate
	}
	if in.ParentKey != "" {
		fields["parent"] = map[string]any{"key": in.ParentKey}
	}

	data, err := c.do(ctx, "create issue", http.MethodPost, c.api("/issue"), nil, map[string]any{"fields": fields})
	if err != nil {
		return nil, err
	}
	var out CreatedIssue
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding create response: %w", err)
	}
	out.URL = c.browseURL(out.Key)
	return &out, nil
}

// renderText returns the dialect-appropriate representation of a rich-text body:
// a plain string on Server/DC, an ADF document on Cloud.
func (c *Cloud) renderText(text string) any {
	if c.dc {
		return text
	}
	return TextToADF(text)
}

// UpdateIssue implements Client.
func (c *Cloud) UpdateIssue(ctx context.Context, key string, in UpdateIssueInput) error {
	fields := map[string]any{}
	for k, v := range in.ExtraFields {
		fields[k] = v
	}
	if in.Summary != "" {
		fields["summary"] = in.Summary
	}
	if in.Description != "" {
		fields["description"] = c.renderText(in.Description)
	}
	if in.AssigneeID != "" {
		fields["assignee"] = c.userRef(in.AssigneeID)
	}
	if in.Priority != "" {
		fields["priority"] = map[string]any{"name": in.Priority}
	}
	if in.Labels != nil {
		fields["labels"] = in.Labels
	}
	if in.Components != nil {
		comps := make([]any, 0, len(in.Components))
		for _, name := range in.Components {
			comps = append(comps, map[string]any{"name": name})
		}
		fields["components"] = comps
	}
	if in.DueDate != "" {
		fields["duedate"] = in.DueDate
	}
	// PUT returns 204 No Content on success; we only care about the error.
	_, err := c.do(ctx, "update issue", http.MethodPut,
		c.api("/issue/"+url.PathEscape(key)), nil, map[string]any{"fields": fields})
	return err
}

// wireComment is the on-the-wire shape of a Jira comment. Body is deferred
// because the dialects disagree on its type: a JSON string on Server/DC, an ADF
// object on Cloud.
type wireComment struct {
	ID     string          `json:"id"`
	Body   json.RawMessage `json:"body"`
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// toComment converts a decoded wire comment into the exported form, flattening
// a Cloud ADF body to plain text.
func (c *Cloud) toComment(w wireComment, key string) Comment {
	return Comment{
		ID:      w.ID,
		Body:    decodeCommentBody(w.Body),
		Author:  w.Author.DisplayName,
		Created: w.Created,
		Updated: w.Updated,
		URL:     c.browseURL(key),
	}
}

// decodeCommentBody renders a raw comment body as plain text, accepting either
// dialect's representation. An unrecognized shape yields an empty string rather
// than an error: a comment we cannot render is still worth listing by id.
func decodeCommentBody(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return ADFToText(doc)
}

// AddComment implements Client.
func (c *Cloud) AddComment(ctx context.Context, key, body string) (*Comment, error) {
	data, err := c.do(ctx, "add comment", http.MethodPost,
		c.api("/issue/"+url.PathEscape(key)+"/comment"), nil,
		map[string]any{"body": c.renderText(body)})
	if err != nil {
		return nil, err
	}
	var w wireComment
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("decoding comment response: %w", err)
	}
	out := c.toComment(w, key)
	return &out, nil
}

// ListComments implements Client. Jira paginates comments oldest first, so the
// newest ones are the LAST page: the count is read first and the window is
// requested by offset. That costs one extra cheap request but is deterministic
// on both dialects, unlike `orderBy`, which Server/DC does not honor reliably.
func (c *Cloud) ListComments(ctx context.Context, key string, limit int) ([]Comment, error) {
	if limit <= 0 {
		limit = 20
	}
	path := c.api("/issue/" + url.PathEscape(key) + "/comment")

	count := url.Values{}
	count.Set("maxResults", "0")
	data, err := c.do(ctx, "list comments", http.MethodGet, path, count, nil)
	if err != nil {
		return nil, err
	}
	var head struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("decoding comment count: %w", err)
	}
	if head.Total == 0 {
		return nil, nil
	}

	start := head.Total - limit
	if start < 0 {
		start = 0
	}
	q := url.Values{}
	q.Set("startAt", strconv.Itoa(start))
	q.Set("maxResults", strconv.Itoa(limit))
	data, err = c.do(ctx, "list comments", http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Comments []wireComment `json:"comments"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decoding comments: %w", err)
	}
	out := make([]Comment, 0, len(page.Comments))
	for _, w := range page.Comments {
		out = append(out, c.toComment(w, key))
	}
	return out, nil
}

// UpdateComment implements Client. The body is REPLACED, not appended to.
func (c *Cloud) UpdateComment(ctx context.Context, key, commentID, body string) (*Comment, error) {
	data, err := c.do(ctx, "update comment", http.MethodPut,
		c.api("/issue/"+url.PathEscape(key)+"/comment/"+url.PathEscape(commentID)), nil,
		map[string]any{"body": c.renderText(body)})
	if err != nil {
		return nil, err
	}
	var w wireComment
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("decoding comment response: %w", err)
	}
	out := c.toComment(w, key)
	return &out, nil
}

// Transitions implements Client.
func (c *Cloud) Transitions(ctx context.Context, key string) ([]Transition, error) {
	data, err := c.do(ctx, "list transitions", http.MethodGet,
		c.api("/issue/"+url.PathEscape(key)+"/transitions"), nil, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("decoding transitions: %w", err)
	}
	out := make([]Transition, 0, len(page.Transitions))
	for _, t := range page.Transitions {
		out = append(out, Transition{ID: t.ID, Name: t.Name, ToName: t.To.Name})
	}
	return out, nil
}

// TransitionIssue implements Client.
func (c *Cloud) TransitionIssue(ctx context.Context, key, transitionID, comment string) error {
	payload := map[string]any{"transition": map[string]any{"id": transitionID}}
	if comment != "" {
		payload["update"] = map[string]any{
			"comment": []any{
				map[string]any{"add": map[string]any{"body": c.renderText(comment)}},
			},
		}
	}
	_, err := c.do(ctx, "transition issue", http.MethodPost,
		c.api("/issue/"+url.PathEscape(key)+"/transitions"), nil, payload)
	return err
}
