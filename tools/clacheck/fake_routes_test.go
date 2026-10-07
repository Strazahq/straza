package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	path := req.URL.Path
	f.log = append(f.log, req.Method+" "+path)
	if fn, ok := f.during[req.Method+" "+path]; ok {
		delete(f.during, req.Method+" "+path)
		fn()
	}
	if fail, ok := f.fail[req.Method+" "+path]; ok {
		delete(f.fail, req.Method+" "+path)
		for k, v := range fail.headers {
			w.Header().Set(k, v)
		}
		reply(w, fail.code, map[string]any{"message": fail.message})
		return
	}
	auth := req.Header.Get("Authorization")
	if strings.HasPrefix(path, "/repos/strazahq/cla-records/") {
		if auth == "Bearer "+testToken {
			f.t.Errorf("the workflow token reached the records repository: %s %s", req.Method, path)
		}
		if auth != "Bearer "+testRecordsToken {
			reply(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
			return
		}
		f.serveRecords(w, req, strings.TrimPrefix(path, "/repos/strazahq/cla-records/contents/"), body)
		return
	}
	if auth == "Bearer "+testRecordsToken {
		f.t.Errorf("the records token reached the pull request's repository: %s %s", req.Method, path)
	}
	if auth != "Bearer "+testToken {
		reply(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
		return
	}
	f.serveRepo(w, req, path, body)
}

func (f *fakeGitHub) serveRecords(w http.ResponseWriter, req *http.Request, rel string, body []byte) {
	switch req.Method {
	case "GET":
		if data, ok := f.records[rel]; ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		var entries []map[string]string
		for p := range f.records {
			if name, ok := strings.CutPrefix(p, rel+"/"); ok && !strings.Contains(name, "/") {
				entries = append(entries, map[string]string{"name": name, "path": p, "type": "file"})
			}
		}
		if len(entries) == 0 {
			reply(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return
		}
		reply(w, http.StatusOK, entries)
	case "PUT":
		for suffix, code := range f.failPut {
			if strings.HasSuffix(rel, suffix) {
				delete(f.failPut, suffix)
				reply(w, code, map[string]any{"message": "Server Error"})
				return
			}
		}
		if _, ok := f.records[rel]; ok {
			reply(w, http.StatusUnprocessableEntity, map[string]any{"message": "Invalid request.\n\n\"sha\" wasn't supplied."})
			return
		}
		var in struct{ Message, Content string }
		if err := json.Unmarshal(body, &in); err != nil || in.Message == "" {
			f.t.Errorf("PUT %s without a commit message: %s", rel, body)
		}
		data, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			f.t.Errorf("PUT %s content is not base64: %v", rel, err)
		}
		f.records[rel] = data
		reply(w, http.StatusCreated, map[string]any{"content": map[string]string{"path": rel}})
	default:
		f.t.Errorf("unexpected %s on the records repository", req.Method)
	}
}

// page answers one page of list, two items at a time, with GitHub's Link header.
func (f *fakeGitHub) page(w http.ResponseWriter, req *http.Request, list []map[string]any) {
	n, _ := strconv.Atoi(req.URL.Query().Get("page"))
	n = max(n, 1)
	from, to := min((n-1)*pageSize, len(list)), min(n*pageSize, len(list))
	if to < len(list) {
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=100&page=%d>; rel="next", <%s%s?page=9>; rel="last"`, f.srv.URL, req.URL.Path, n+1, f.srv.URL, req.URL.Path))
	}
	reply(w, http.StatusOK, list[from:to])
}

func (f *fakeGitHub) serveRepo(w http.ResponseWriter, req *http.Request, path string, body []byte) {
	const repo = "/repos/strazahq/straza"
	switch {
	case req.Method == "GET" && path == repo+"/pulls/123":
		if req.Header.Get("Accept") == diffAccept {
			if f.diffCode != 0 {
				reply(w, f.diffCode, map[string]any{"message": "Sorry, the diff exceeded the maximum number of lines (20000)"})
				return
			}
			_, _ = w.Write([]byte(f.diff))
			return
		}
		reply(w, http.StatusOK, f.pr)
	case req.Method == "GET" && path == repo+"/pulls":
		if req.URL.Query().Get("state") != "open" {
			f.t.Errorf("open pull requests listed without state=open: %s", req.URL.RawQuery)
		}
		f.page(w, req, f.openPRs)
	case req.Method == "GET" && path == repo+"/contents/CLA.md":
		if req.URL.Query().Get("ref") != testTextCommit || req.Header.Get("Accept") != rawAccept {
			f.t.Errorf("published text read at %q with %q", req.URL.RawQuery, req.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte(f.published))
	case req.Method == "GET" && strings.HasPrefix(path, repo+"/commits/") && strings.HasSuffix(path, "/statuses"):
		sha := strings.TrimSuffix(strings.TrimPrefix(path, repo+"/commits/"), "/statuses")
		n := f.statusCount(sha)
		if req.URL.Query().Get("per_page") != "1" {
			f.t.Errorf("statuses counted with per_page=%q", req.URL.Query().Get("per_page"))
		}
		if n > 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=1&page=2>; rel="next", <%s%s?per_page=1&page=%d>; rel="last"`, f.srv.URL, path, f.srv.URL, path, n))
		}
		reply(w, http.StatusOK, make([]map[string]string, min(n, 1)))
	case req.Method == "POST" && strings.HasPrefix(path, repo+"/statuses/"):
		if f.statusCount(strings.TrimPrefix(path, repo+"/statuses/")) >= 1000 {
			reply(w, http.StatusUnprocessableEntity, map[string]any{"message": "This SHA and context has reached the maximum number of statuses."})
			return
		}
		var s map[string]string
		_ = json.Unmarshal(body, &s)
		if len(s["description"]) > 140 {
			f.t.Errorf("status description over 140 characters: %q", s["description"])
		}
		f.statuses = append(f.statuses, fakeStatus{SHA: strings.TrimPrefix(path, repo+"/statuses/"), State: s["state"], Description: s["description"], Target: s["target_url"]})
		reply(w, http.StatusCreated, s)
	case req.Method == "GET" && path == repo+"/issues/123/comments":
		f.page(w, req, f.lists[kindComment])
	case req.Method == "GET" && path == repo+"/pulls/123/comments":
		f.page(w, req, f.lists[kindReviewComment])
	case req.Method == "GET" && path == repo+"/pulls/123/reviews":
		f.page(w, req, f.lists[kindReview])
	case req.Method == "POST" && path == repo+"/issues/123/comments":
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		id := f.addLocked(kindComment, account{ID: botID, Login: "github-actions[bot]"}, in["body"])
		reply(w, http.StatusCreated, f.comment(id))
	case req.Method == "PATCH" && strings.HasPrefix(path, repo+"/issues/comments/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, repo+"/issues/comments/"), 10, 64)
		var in map[string]string
		_ = json.Unmarshal(body, &in)
		f.comment(id)["body"] = in["body"]
		f.edits++
		reply(w, http.StatusOK, f.comment(id))
	case req.Method == "PUT" && path == repo+"/issues/123/lock":
		f.pr["locked"] = true
		w.WriteHeader(http.StatusNoContent)
	case req.Method == "POST" && path == "/graphql":
		f.serveGraphQL(w, body)
	default:
		f.t.Errorf("unexpected request %s %s", req.Method, path)
		reply(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	}
}

// serveGraphQL answers the commits query, the query for one commit's further
// authors and the edit history query, in the shapes GitHub gave live.
func (f *fakeGitHub) serveGraphQL(w http.ResponseWriter, body []byte) {
	var in struct {
		Query     string
		Variables struct {
			Owner, Name, Cursor, OID, ID string
			Number                       int
			IDs                          []string
		}
	}
	_ = json.Unmarshal(body, &in)
	switch {
	case strings.Contains(in.Query, "userContentEdits") && in.Variables.ID != "":
		edits := f.editsPage(f.history[in.Variables.ID], in.Variables.Cursor, strings.Contains(in.Query, "editor"))
		reply(w, http.StatusOK, map[string]any{"data": map[string]any{"node": map[string]any{"userContentEdits": edits}}})
	case strings.Contains(in.Query, "userContentEdits"):
		var nodes []any
		var errs []any
		for _, id := range in.Variables.IDs {
			revs, known := f.history[id]
			if !known && !f.nodeExists(id) {
				nodes = append(nodes, nil)
				errs = append(errs, map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to a node with the global id of '" + id + "'"})
				continue
			}
			var last any
			if len(revs) > 0 {
				last = revs[0]["editedAt"]
			}
			nodes = append(nodes, map[string]any{"id": id, "lastEditedAt": last, "userContentEdits": f.editsPage(revs, "", strings.Contains(in.Query, "editor"))})
		}
		answer := map[string]any{"data": map[string]any{"nodes": nodes}}
		if len(errs) > 0 {
			answer["errors"] = errs
		}
		reply(w, http.StatusOK, answer)
	case strings.Contains(in.Query, "object(oid"):
		for _, c := range f.commits {
			if c.OID == in.Variables.OID {
				authors := f.authorsPage(c.Authors, in.Variables.Cursor)
				reply(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"object": map[string]any{"authors": authors}}}})
				return
			}
		}
		f.t.Errorf("authors asked for an unknown commit %s", in.Variables.OID)
	default:
		if in.Variables.Owner != "strazahq" || in.Variables.Name != "straza" || in.Variables.Number != 123 {
			f.t.Errorf("unexpected commits query variables %+v", in.Variables)
		}
		from, _ := strconv.Atoi(in.Variables.Cursor)
		to := min(from+pageSize, len(f.commits))
		var nodes []any
		for _, c := range f.commits[from:to] {
			nodes = append(nodes, map[string]any{"commit": map[string]any{"oid": c.OID, "message": c.Message, "authors": f.authorsPage(c.Authors, "")}})
		}
		page := map[string]any{"pageInfo": map[string]any{"hasNextPage": to < len(f.commits), "endCursor": strconv.Itoa(to)}, "nodes": nodes}
		reply(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"commits": page}}}})
	}
}

// editsPage answers a page of an edit history, newest first, two at a time.
// It names each revision's editor only when the query asks for it.
func (f *fakeGitHub) editsPage(revs []map[string]any, cursor string, editor bool) map[string]any {
	from, _ := strconv.Atoi(cursor)
	from = min(from, len(revs))
	to := min(from+pageSize, len(revs))
	var nodes []map[string]any
	for _, r := range revs[from:to] {
		n := map[string]any{"editedAt": r["editedAt"], "diff": r["diff"]}
		if editor {
			n["editor"] = r["editor"]
		}
		nodes = append(nodes, n)
	}
	return map[string]any{"pageInfo": map[string]any{"hasNextPage": to < len(revs), "endCursor": strconv.Itoa(to)}, "nodes": nodes}
}

// statusCount is how many statuses the commit holds.
func (f *fakeGitHub) statusCount(sha string) int {
	n := f.statusBase[sha]
	for _, s := range f.statuses {
		if s.SHA == sha {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) nodeExists(id string) bool {
	for _, list := range f.lists {
		for _, c := range list {
			if fmt.Sprint(c["node_id"]) == id {
				return true
			}
		}
	}
	return false
}

// authorsPage answers a page of a commit's authors, two at a time.
func (f *fakeGitHub) authorsPage(all []actor, cursor string) map[string]any {
	from, _ := strconv.Atoi(cursor)
	to := min(from+pageSize, len(all))
	var nodes []any
	for _, a := range all[from:to] {
		var user any
		if a.ID != 0 {
			user = map[string]any{"databaseId": a.ID, "login": a.Login}
		}
		nodes = append(nodes, map[string]any{"name": a.Name, "email": a.Email, "user": user})
	}
	return map[string]any{"pageInfo": map[string]any{"hasNextPage": to < len(all), "endCursor": strconv.Itoa(to)}, "nodes": nodes}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(mustJSON(v))
}
