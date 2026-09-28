package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// capture records the last request a test server received.
type capture struct {
	method string
	path   string
	rawQ   string
	auth   string
	xsrf   string
	body   map[string]any
}

func newServer(t *testing.T, status int, respBody string, cap *capture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQ = r.URL.RawQuery
		cap.auth = r.Header.Get("Authorization")
		cap.xsrf = r.Header.Get("X-Atlassian-Token")
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &cap.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dcClient(url string) *RESTClient {
	return NewRESTClient(url, "dc", "", "", "pat-123", 5*time.Second)
}
func cloudClient(url string) *RESTClient {
	return NewRESTClient(url, "cloud", "svc@yildizozan.com", "tok", "", 5*time.Second)
}

// The bug the live review caught: CreateIssue must honor the API version, not
// hardcode v3. On DC it must POST to /rest/api/2/issue with a {"name": ...}
// reporter and a plain-string description.
func TestCreateIssue_DC_PathPayloadAndAuth(t *testing.T) {
	var cap capture
	srv := newServer(t, 201, `{"id":"1000","key":"DPS-7"}`, &cap)
	c := dcClient(srv.URL)

	out, err := c.CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey: "DPS", IssueTypeID: "10202", Summary: "x",
		Description: "line one", ReporterID: "ozan.yildiz", AssigneeID: "jane",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cap.path != "/rest/api/2/issue" {
		t.Fatalf("DC create must hit /rest/api/2/issue, got %q", cap.path)
	}
	if cap.auth != "Bearer pat-123" {
		t.Fatalf("expected bearer auth, got %q", cap.auth)
	}
	if cap.xsrf != "no-check" {
		t.Fatalf("mutating DC request must send X-Atlassian-Token: no-check, got %q", cap.xsrf)
	}
	fields, _ := cap.body["fields"].(map[string]any)
	if rep, _ := fields["reporter"].(map[string]any); rep["name"] != "ozan.yildiz" {
		t.Fatalf("DC reporter must be {name}, got %v", fields["reporter"])
	}
	if asg, _ := fields["assignee"].(map[string]any); asg["name"] != "jane" {
		t.Fatalf("DC assignee must be {name}, got %v", fields["assignee"])
	}
	if desc, ok := fields["description"].(string); !ok || desc != "line one" {
		t.Fatalf("DC description must be a plain string, got %T %v", fields["description"], fields["description"])
	}
	if out.Key != "DPS-7" || out.URL != srv.URL+"/browse/DPS-7" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestCreateIssue_Cloud_PathPayloadAndADF(t *testing.T) {
	var cap capture
	srv := newServer(t, 201, `{"id":"1","key":"PAY-1"}`, &cap)
	c := cloudClient(srv.URL)

	if _, err := c.CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey: "PAY", IssueTypeID: "1", Summary: "x",
		Description: "hi", ReporterID: "acc-1",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if cap.path != "/rest/api/3/issue" {
		t.Fatalf("Cloud create must hit /rest/api/3/issue, got %q", cap.path)
	}
	fields, _ := cap.body["fields"].(map[string]any)
	if rep, _ := fields["reporter"].(map[string]any); rep["accountId"] != "acc-1" {
		t.Fatalf("Cloud reporter must be {accountId}, got %v", fields["reporter"])
	}
	if doc, ok := fields["description"].(map[string]any); !ok || doc["type"] != "doc" {
		t.Fatalf("Cloud description must be ADF doc, got %T", fields["description"])
	}
}

func TestMyself_DC_UsesV2AndParsesName(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"name":"ozan.yildiz","key":"JIRAUSER41400","displayName":"Ozan","active":true}`, &cap)
	u, err := dcClient(srv.URL).Myself(context.Background())
	if err != nil {
		t.Fatalf("myself: %v", err)
	}
	if cap.path != "/rest/api/2/myself" {
		t.Fatalf("DC myself path: %q", cap.path)
	}
	if u.Ref() != "ozan.yildiz" {
		t.Fatalf("Ref() should be username when accountId empty, got %q", u.Ref())
	}
}

func TestUserRef(t *testing.T) {
	cases := []struct {
		name string
		u    User
		want string
	}{
		{"cloud accountId wins", User{AccountID: "acc-1", Name: "ozan.yildiz", Key: "JIRAUSER1"}, "acc-1"},
		{"dc username when accountId empty", User{Name: "ozan.yildiz", Key: "JIRAUSER41400"}, "ozan.yildiz"},
		// The opaque DC key must NOT be used as a reporter id (it is invalid in
		// the `name` field); a key-only user has no usable reference.
		{"dc key is not a fallback", User{Key: "JIRAUSER41400", DisplayName: "Ghost"}, ""},
		{"empty user", User{}, ""},
	}
	for _, tc := range cases {
		if got := tc.u.Ref(); got != tc.want {
			t.Errorf("%s: Ref()=%q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSearchUsers_DC_UsernameParam(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `[{"name":"ozan.yildiz","key":"JIRAUSER41400","active":true}]`, &cap)
	users, err := dcClient(srv.URL).SearchUsers(context.Background(), "ozan")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if cap.path != "/rest/api/2/user/search" || cap.rawQ == "" || !contains(cap.rawQ, "username=ozan") {
		t.Fatalf("DC user search must use ?username=, got path=%q q=%q", cap.path, cap.rawQ)
	}
	if len(users) != 1 || users[0].Ref() != "ozan.yildiz" {
		t.Fatalf("unexpected users: %+v", users)
	}
}

func TestIssueTypes_DC_FromProject(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"key":"DOSD","issueTypes":[{"id":"11302","name":"Service Request"}]}`, &cap)
	types, err := dcClient(srv.URL).IssueTypes(context.Background(), "DOSD")
	if err != nil {
		t.Fatalf("issuetypes: %v", err)
	}
	if cap.path != "/rest/api/2/project/DOSD" {
		t.Fatalf("DC issue types must come from /rest/api/2/project/{key}, got %q", cap.path)
	}
	if len(types) != 1 || types[0].Name != "Service Request" {
		t.Fatalf("unexpected types: %+v", types)
	}
}

func TestSearchProjects_DC_ArrayAndFilter(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `[{"id":"1","key":"DOSD","name":"DevOps Service Desk"},{"id":"2","key":"AC","name":"Agile"}]`, &cap)
	got, err := dcClient(srv.URL).SearchProjects(context.Background(), "dosd")
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	if cap.path != "/rest/api/2/project" {
		t.Fatalf("DC project list path: %q", cap.path)
	}
	if len(got) != 1 || got[0].Key != "DOSD" {
		t.Fatalf("client-side filter failed: %+v", got)
	}
}

// On DC, CreateMeta must FAIL OPEN unconditionally and WITHOUT a network call:
// it returns (nil, error) so the caller skips pre-validation and never blocks
// the reporter on a metadata pre-check.
func TestCreateMeta_DC_FailsOpenWithoutCall(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"projects":[{"issuetypes":[{"fields":{}}]}]}`, &cap)
	meta, err := dcClient(srv.URL).CreateMeta(context.Background(), "DOSD", "11302")
	if err == nil {
		t.Fatal("DC CreateMeta must return an error so the caller fails open")
	}
	if meta != nil {
		t.Fatalf("meta must be nil on DC, got %+v", meta)
	}
	if cap.path != "" {
		t.Fatalf("DC CreateMeta must not make an HTTP call, hit %q", cap.path)
	}
}

// ExtraFields (a team's custom fields) must never override core routing/identity
// fields, even if validation were bypassed.
func TestCreateIssue_ExtraFieldsCannotOverrideCore(t *testing.T) {
	var cap capture
	srv := newServer(t, 201, `{"id":"1","key":"DPS-9"}`, &cap)
	_, err := dcClient(srv.URL).CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey: "DPS", IssueTypeID: "10202", Summary: "real",
		ExtraFields: map[string]any{
			"project":           map[string]any{"key": "EVIL"},
			"summary":           "hijacked",
			"customfield_10010": map[string]any{"value": "ok"},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fields, _ := cap.body["fields"].(map[string]any)
	if p, _ := fields["project"].(map[string]any); p["key"] != "DPS" {
		t.Fatalf("ExtraFields must not override project, got %v", fields["project"])
	}
	if fields["summary"] != "real" {
		t.Fatalf("ExtraFields must not override summary, got %v", fields["summary"])
	}
	if cf, _ := fields["customfield_10010"].(map[string]any); cf["value"] != "ok" {
		t.Fatalf("legit custom field dropped: %v", fields["customfield_10010"])
	}
}

func TestUpdateIssue_DC_PutPathAndPayload(t *testing.T) {
	var cap capture
	srv := newServer(t, 204, ``, &cap)
	err := dcClient(srv.URL).UpdateIssue(context.Background(), "DOSD-1", UpdateIssueInput{
		Summary: "new", Description: "body", AssigneeID: "jane", Labels: []string{"x"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if cap.method != "PUT" || cap.path != "/rest/api/2/issue/DOSD-1" {
		t.Fatalf("DC update must PUT /rest/api/2/issue/DOSD-1, got %s %s", cap.method, cap.path)
	}
	if cap.xsrf != "no-check" {
		t.Fatalf("update must send X-Atlassian-Token: no-check, got %q", cap.xsrf)
	}
	fields, _ := cap.body["fields"].(map[string]any)
	if fields["summary"] != "new" {
		t.Fatalf("summary not set: %v", fields["summary"])
	}
	if asg, _ := fields["assignee"].(map[string]any); asg["name"] != "jane" {
		t.Fatalf("DC assignee must be {name}, got %v", fields["assignee"])
	}
	if _, ok := fields["description"].(string); !ok {
		t.Fatalf("DC description must be a plain string, got %T", fields["description"])
	}
}

func TestAddComment_DialectBody(t *testing.T) {
	var cap capture
	// DC: plain string body.
	srv := newServer(t, 201, `{"id":"9001"}`, &cap)
	c, err := dcClient(srv.URL).AddComment(context.Background(), "DOSD-1", "hi there")
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	if cap.path != "/rest/api/2/issue/DOSD-1/comment" || cap.method != "POST" {
		t.Fatalf("comment path: %s %s", cap.method, cap.path)
	}
	if _, ok := cap.body["body"].(string); !ok {
		t.Fatalf("DC comment body must be a string, got %T", cap.body["body"])
	}
	if c.ID != "9001" {
		t.Fatalf("comment id: %q", c.ID)
	}

	// Cloud: ADF body.
	var cap2 capture
	srv2 := newServer(t, 201, `{"id":"1"}`, &cap2)
	if _, err := cloudClient(srv2.URL).AddComment(context.Background(), "PAY-1", "hi"); err != nil {
		t.Fatalf("cloud comment: %v", err)
	}
	if doc, ok := cap2.body["body"].(map[string]any); !ok || doc["type"] != "doc" {
		t.Fatalf("Cloud comment body must be ADF, got %T", cap2.body["body"])
	}
}

func TestTransitions_ListAndApply(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"transitions":[{"id":"11","name":"Start Progress","to":{"name":"In Progress"}}]}`, &cap)
	c := dcClient(srv.URL)
	ts, err := c.Transitions(context.Background(), "DOSD-1")
	if err != nil {
		t.Fatalf("transitions: %v", err)
	}
	if cap.path != "/rest/api/2/issue/DOSD-1/transitions" {
		t.Fatalf("transitions path: %s", cap.path)
	}
	if len(ts) != 1 || ts[0].ID != "11" || ts[0].ToName != "In Progress" {
		t.Fatalf("unexpected transitions: %+v", ts)
	}

	var cap2 capture
	srv2 := newServer(t, 204, ``, &cap2)
	if err := dcClient(srv2.URL).TransitionIssue(context.Background(), "DOSD-1", "11", "moving on"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cap2.method != "POST" || cap2.path != "/rest/api/2/issue/DOSD-1/transitions" {
		t.Fatalf("apply path: %s %s", cap2.method, cap2.path)
	}
	if tr, _ := cap2.body["transition"].(map[string]any); tr["id"] != "11" {
		t.Fatalf("transition id not sent: %v", cap2.body["transition"])
	}
	if _, ok := cap2.body["update"]; !ok {
		t.Fatal("comment should be sent via update.comment")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ListComments must page to the END of the comment list (newest comments are
// last) and must decode both body dialects.
func TestListComments_DC_OffsetAndPlainBody(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"total":50,"comments":[
		{"id":"9001","body":"plain text","author":{"displayName":"Ozan"},"created":"2026-09-18T10:00:00.000+0300","updated":"2026-09-18T10:00:00.000+0300"}]}`, &cap)
	c := dcClient(srv.URL)

	got, err := c.ListComments(context.Background(), "DOSD-1", 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if cap.method != "GET" || cap.path != "/rest/api/2/issue/DOSD-1/comment" {
		t.Fatalf("DC list path: %s %s", cap.method, cap.path)
	}
	// total=50, limit=20 -> the window must start at 30, not 0.
	if cap.rawQ != "maxResults=20&startAt=30" {
		t.Fatalf("expected the last page to be requested, got query %q", cap.rawQ)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(got))
	}
	if got[0].Body != "plain text" || got[0].Author != "Ozan" || got[0].ID != "9001" {
		t.Fatalf("unexpected comment: %+v", got[0])
	}
	if got[0].URL != srv.URL+"/browse/DOSD-1" {
		t.Fatalf("comment URL should point at the issue, got %q", got[0].URL)
	}
}

func TestListComments_Cloud_ADFBody(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"total":1,"comments":[
		{"id":"9002","body":{"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"from cloud"}]}]},
		 "author":{"displayName":"Jane"}}]}`, &cap)

	got, err := cloudClient(srv.URL).ListComments(context.Background(), "DOSD-1", 5)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if cap.path != "/rest/api/3/issue/DOSD-1/comment" {
		t.Fatalf("cloud list must use v3, got %q", cap.path)
	}
	if len(got) != 1 || got[0].Body != "from cloud" {
		t.Fatalf("ADF body not flattened: %+v", got)
	}
}

func TestListComments_Empty_SkipsSecondRequest(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"total":0,"comments":[]}`, &cap)

	got, err := dcClient(srv.URL).ListComments(context.Background(), "DOSD-1", 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no comments, got %+v", got)
	}
	if cap.rawQ != "maxResults=0" {
		t.Fatalf("only the count request should have been sent, last query was %q", cap.rawQ)
	}
}

func TestUpdateComment_DC_PathAndPlainBody(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"id":"9001","body":"edited","author":{"displayName":"Ozan"}}`, &cap)

	got, err := dcClient(srv.URL).UpdateComment(context.Background(), "DOSD-1", "9001", "edited")
	if err != nil {
		t.Fatalf("update comment: %v", err)
	}
	if cap.method != "PUT" || cap.path != "/rest/api/2/issue/DOSD-1/comment/9001" {
		t.Fatalf("DC update path: %s %s", cap.method, cap.path)
	}
	if cap.xsrf != "no-check" {
		t.Fatalf("mutating DC request must send X-Atlassian-Token: no-check, got %q", cap.xsrf)
	}
	if cap.body["body"] != "edited" {
		t.Fatalf("DC body must be a plain string, got %#v", cap.body["body"])
	}
	if got.ID != "9001" || got.Body != "edited" {
		t.Fatalf("unexpected comment: %+v", got)
	}
}

func TestUpdateComment_Cloud_ADFBody(t *testing.T) {
	var cap capture
	srv := newServer(t, 200, `{"id":"9002","body":{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"edited"}]}]}}`, &cap)

	got, err := cloudClient(srv.URL).UpdateComment(context.Background(), "DOSD-1", "9002", "edited")
	if err != nil {
		t.Fatalf("update comment: %v", err)
	}
	if cap.path != "/rest/api/3/issue/DOSD-1/comment/9002" {
		t.Fatalf("cloud update must use v3, got %q", cap.path)
	}
	doc, ok := cap.body["body"].(map[string]any)
	if !ok || doc["type"] != "doc" {
		t.Fatalf("cloud body must be an ADF document, got %#v", cap.body["body"])
	}
	if got.Body != "edited" {
		t.Fatalf("response body not flattened: %+v", got)
	}
}

func TestUpdateComment_PermissionDeniedSurfaces(t *testing.T) {
	var cap capture
	srv := newServer(t, 403, `{"errorMessages":["You do not have permission to edit this comment."],"errors":{}}`, &cap)

	_, err := dcClient(srv.URL).UpdateComment(context.Background(), "DOSD-1", "9001", "edited")
	if err == nil {
		t.Fatal("expected an error for HTTP 403")
	}
	if !contains(err.Error(), "permission denied") || !contains(err.Error(), "update comment") {
		t.Fatalf("403 should surface as an actionable permission error, got %q", err.Error())
	}
}
