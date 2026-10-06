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

// RESTClient is the net/http-backed implementation of Client. It speaks both
// dialects: Jira Cloud (REST API v3, ADF, accountId) under basic
// auth, and Jira Server/Data Center (REST API v2, plain-text, username/key)
// under bearer-PAT auth. The dialect is selected by authMode at construction.
type RESTClient struct {
	baseURL    string
	authHeader string
	dc         bool // Data Center / Server dialect (REST v2) vs Cloud (v3)
	ver        string
	http       *http.Client
}

// NewRESTClient builds a client. authMode is "cloud" (basic email:token, REST v3) or
// "dc" (bearer PAT, REST v2).
func NewRESTClient(baseURL, authMode, email, apiToken, pat string, timeout time.Duration) *RESTClient {
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
	return &RESTClient{
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
func (c *RESTClient) api(suffix string) string {
	return "/rest/api/" + c.ver + suffix
}

// browseURL builds the human-facing URL for an issue key.
func (c *RESTClient) browseURL(key string) string {
	return c.baseURL + "/browse/" + key
}

// BrowseURL implements Client.
func (c *RESTClient) BrowseURL(key string) string { return c.browseURL(key) }

// userRef wraps a resolved user identifier in the dialect-appropriate object
// for an issue field: {"name": ...} for DC/Server, {"accountId": ...} for Cloud.
func (c *RESTClient) userRef(id string) map[string]any {
	if c.dc {
		return map[string]any{"name": id}
	}
	return map[string]any{"accountId": id}
}

// renderText returns the dialect-appropriate representation of a rich-text body:
// a plain string (wiki markup) on Server/DC REST v2, an ADF document on Cloud
// REST v3.
func (c *RESTClient) renderText(text string) any {
	if c.dc {
		return text
	}
	return TextToADF(text)
}

// componentRefs wraps component names in the {"name": ...} objects Jira expects.
func componentRefs(names []string) []any {
	refs := make([]any, 0, len(names))
	for _, name := range names {
		refs = append(refs, map[string]any{"name": name})
	}
	return refs
}

// do executes a request and returns the response body for 2xx, or an *APIError.
func (c *RESTClient) do(ctx context.Context, op, method, path string, query url.Values, body any) ([]byte, error) {
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
func (c *RESTClient) Myself(ctx context.Context) (*User, error) {
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
func (c *RESTClient) SearchUsers(ctx context.Context, query string) ([]User, error) {
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
func (c *RESTClient) SearchProjects(ctx context.Context, query string) ([]Project, error) {
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
func (c *RESTClient) IssueTypes(ctx context.Context, projectKey string) ([]IssueType, error) {
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
func (c *RESTClient) CreateMeta(ctx context.Context, projectKey, issueTypeID string) (*CreateMeta, error) {
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
func (c *RESTClient) CreateIssue(ctx context.Context, in CreateIssueInput) (*CreatedIssue, error) {
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
		fields["description"] = c.renderText(in.Description)
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
		fields["components"] = componentRefs(in.Components)
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

// issueFields is the field list GetIssue requests, so Jira does not send every
// custom field on the issue.
const issueFields = "summary,description,status,issuetype,priority,assignee,reporter," +
	"labels,components,duedate,parent,created,updated"

// named is the {"name": ...} shape Jira uses for status, priority, etc.
type named struct {
	Name string `json:"name"`
}

// person is the part of a Jira user object GetIssue reports.
type person struct {
	DisplayName string `json:"displayName"`
}

// GetIssue implements Client. Optional fields Jira returns as null (assignee,
// priority, parent) decode to empty strings.
func (c *RESTClient) GetIssue(ctx context.Context, key string) (*Issue, error) {
	q := url.Values{}
	q.Set("fields", issueFields)
	data, err := c.do(ctx, "get issue", http.MethodGet, c.api("/issue/"+url.PathEscape(key)), q, nil)
	if err != nil {
		return nil, err
	}
	var w struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
			Status      named           `json:"status"`
			IssueType   named           `json:"issuetype"`
			Priority    named           `json:"priority"`
			Assignee    person          `json:"assignee"`
			Reporter    person          `json:"reporter"`
			Labels      []string        `json:"labels"`
			Components  []named         `json:"components"`
			DueDate     string          `json:"duedate"`
			Parent      struct {
				Key string `json:"key"`
			} `json:"parent"`
			Created string `json:"created"`
			Updated string `json:"updated"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("decoding issue: %w", err)
	}
	f := w.Fields
	components := make([]string, 0, len(f.Components))
	for _, comp := range f.Components {
		components = append(components, comp.Name)
	}
	labels := f.Labels
	if labels == nil {
		labels = []string{}
	}
	return &Issue{
		Key:         w.Key,
		Summary:     f.Summary,
		Description: decodeRichText(f.Description),
		Status:      f.Status.Name,
		IssueType:   f.IssueType.Name,
		Priority:    f.Priority.Name,
		Assignee:    f.Assignee.DisplayName,
		Reporter:    f.Reporter.DisplayName,
		Labels:      labels,
		Components:  components,
		DueDate:     f.DueDate,
		Parent:      f.Parent.Key,
		Created:     f.Created,
		Updated:     f.Updated,
		URL:         c.browseURL(w.Key),
	}, nil
}

// IssueKey implements Client. Only the summary field is requested, to keep the
// response small; the key is always part of the issue resource.
func (c *RESTClient) IssueKey(ctx context.Context, key string) (string, error) {
	q := url.Values{}
	q.Set("fields", "summary")
	data, err := c.do(ctx, "get issue key", http.MethodGet, c.api("/issue/"+url.PathEscape(key)), q, nil)
	if err != nil {
		return "", err
	}
	var w struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return "", fmt.Errorf("decoding issue key: %w", err)
	}
	if w.Key == "" {
		return "", fmt.Errorf("get issue key: Jira returned no key for %s", key)
	}
	return w.Key, nil
}

// UpdateIssue implements Client.
func (c *RESTClient) UpdateIssue(ctx context.Context, key string, in UpdateIssueInput) error {
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
		fields["components"] = componentRefs(in.Components)
	}
	if in.DueDate != "" {
		fields["duedate"] = in.DueDate
	}
	// Jira empties a field that is set to null.
	for _, id := range in.Clear {
		fields[id] = nil
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
func (c *RESTClient) toComment(w wireComment, key string) Comment {
	return Comment{
		ID:      w.ID,
		Body:    decodeRichText(w.Body),
		Author:  w.Author.DisplayName,
		Created: w.Created,
		Updated: w.Updated,
		URL:     c.browseURL(key),
	}
}

// decodeRichText renders a raw rich-text field (comment body, description) as
// plain text, accepting either dialect's representation. An unrecognized shape
// or JSON null yields an empty string rather than an error: an issue or comment
// we cannot render is still worth returning.
func decodeRichText(raw json.RawMessage) string {
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
func (c *RESTClient) AddComment(ctx context.Context, key, body string) (*Comment, error) {
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
func (c *RESTClient) ListComments(ctx context.Context, key string, limit int) ([]Comment, error) {
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
func (c *RESTClient) UpdateComment(ctx context.Context, key, commentID, body string) (*Comment, error) {
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
func (c *RESTClient) Transitions(ctx context.Context, key string) ([]Transition, error) {
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
func (c *RESTClient) TransitionIssue(ctx context.Context, key, transitionID, comment string) error {
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
