package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/obs"
	"pcoder/internal/project"
)

func TestObserveRoundTrip(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	id := "x/hello"
	ctx := obs.WithProject(context.Background(), id)
	esc := "/api/projects/" + url.PathEscape(id)

	rec := authedGet(t, h, cookie, esc+"/observe")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty observe: %d", rec.Code)
	}
	var empty struct {
		Logs     []obs.Entry `json:"logs"`
		FirstSeq int64       `json:"firstSeq"`
		LastSeq  int64       `json:"lastSeq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || empty.Logs == nil || len(empty.Logs) != 0 {
		t.Fatalf("empty body = %q", rec.Body.String())
	}

	// write via the staged API (handlers do the same)
	obs.Info(ctx, obs.PreviewStart, "Preview started on :3000", map[string]any{"port": 3000})
	obs.Info(ctx, obs.ProjectImageBuild, "build failed attempt 1", nil)

	rec = authedGet(t, h, cookie, esc+"/observe")
	var body struct {
		Logs     []obs.Entry `json:"logs"`
		FirstSeq int64       `json:"firstSeq"`
		LastSeq  int64       `json:"lastSeq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Logs) != 2 {
		t.Fatalf("observe = %q err=%v", rec.Body.String(), err)
	}
	if body.FirstSeq != 1 || body.LastSeq != 2 || body.Logs[0].Seq != 1 {
		t.Fatalf("cursors = %+v", body)
	}

	// after cursor tails newer only
	rec = authedGet(t, h, cookie, esc+"/observe?after=1")
	var tail struct {
		Logs []obs.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tail); err != nil || len(tail.Logs) != 1 {
		t.Fatalf("after=1 = %q", rec.Body.String())
	}
	// before cursor pages older
	rec = authedGet(t, h, cookie, esc+"/observe?before=2&limit=200")
	var older struct {
		Logs []obs.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &older); err != nil || len(older.Logs) != 1 {
		t.Fatalf("before=2 = %q", rec.Body.String())
	}
	// facet + search filters
	rec = authedGet(t, h, cookie, esc+"/observe?type=project.image.build")
	var filt struct {
		Logs []obs.Entry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filt); err != nil || len(filt.Logs) != 1 {
		t.Fatalf("type filter = %q", rec.Body.String())
	}
	rec = authedGet(t, h, cookie, esc+"/observe?q=started+on+%3A3000")
	if err := json.Unmarshal(rec.Body.Bytes(), &filt); err != nil || len(filt.Logs) != 1 {
		t.Fatalf("q filter = %q", rec.Body.String())
	}

	if rec := authedGet(t, h, cookie, esc+"/observe?after=nope"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad after: %d, want 400", rec.Code)
	}
	if rec := get(t, h, esc+"/observe"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed: %d, want 401", rec.Code)
	}
}

func TestObserveErrors(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	id := "x/hello"
	ctx := obs.WithProject(context.Background(), id)
	esc := "/api/projects/" + url.PathEscape(id)
	obs.Info(ctx, obs.PreviewStart, "Preview started", nil)
	obs.Error(ctx, obs.ProjectClone, "clone failed sha abc1234 attempt 1", nil)
	obs.Error(ctx, obs.ProjectClone, "clone failed sha deadbee attempt 2", nil)
	rec := authedGet(t, h, cookie, esc+"/observe/errors")
	var body struct {
		Groups []obs.Group `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Groups) != 1 || body.Groups[0].Count != 2 {
		t.Fatalf("groups = %+v", body.Groups)
	}
}

func TestObserveMeta(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/observe/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("meta: %d %q", rec.Code, rec.Body.String())
	}
	var body struct {
		AuditTypes []string `json:"auditTypes"`
		BuildTypes []string `json:"buildTypes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.AuditTypes) == 0 || len(body.BuildTypes) == 0 {
		t.Fatalf("empty taxonomy: %+v", body)
	}
	// every taxonomy key must be a real obs key (spot-check the milestone
	// set against keys the handlers actually emit).
	for _, k := range append(body.AuditTypes, body.BuildTypes...) {
		if k == "" || len(k) < 3 {
			t.Fatalf("bad key %q", k)
		}
	}
}

func TestObserveStats(t *testing.T) {
	d, pinOut := newTestDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/"+url.PathEscape("x/hello")+"/observe/stats")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %q", rec.Code, rec.Body.String())
	}
	var s obs.ResourceSample
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || s.State != "missing" {
		t.Fatalf("stats = %q err=%v", rec.Body.String(), err)
	}
}

func TestObserveStatsRunning(t *testing.T) {
	d, md, pinOut, st := newProjectDeps(t)
	seedProject(t, st, "abc")
	d.Docker = md
	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").
		Return(docker.Container{Running: true}, nil)
	md.EXPECT().Stats(mock.Anything, "pcoder-abc").
		Return(docker.ContainerStats{
			CPUPercent: 12.5, MemUsed: 512 << 20, MemLimit: 16 << 30,
			NetRX: 100, NetTX: 200, BlockRead: 1024, BlockWrite: 2048, PIDs: 7,
		}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc",
		[]string{"df", "-k", "/workspace"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "Filesystem 1K-blocks Used Available Use% Mounted\n/dev/x 1000 200 800 20% /workspace"}, nil)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/abc/observe/stats")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %q", rec.Code, rec.Body.String())
	}
	var s obs.ResourceSample
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.State != "running" || s.CPUPercent != 12.5 || s.MemUsed != 512<<20 || s.MemLimit != 16<<30 {
		t.Fatalf("cpu/mem missing: %+v", s)
	}
	if s.NetRX != 100 || s.NetTX != 200 || s.BlockR != 1024 || s.BlockW != 2048 || s.PIDs != 7 {
		t.Fatalf("net/blkio/pids missing: %+v", s)
	}
	if s.DiskUsed != 200*1024 || s.DiskTotal != 1000*1024 {
		t.Fatalf("disk missing: %+v", s)
	}
}

func TestObserveCreateSharesTrace(t *testing.T) {
	d, md, pinOut, _ := newProjectDeps(t)
	h := New(d)
	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, project.ProjectImage).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid", []string{"git", "clone", "https://github.com/x/hello.git", "/workspace/repo"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	cookie := loginCookie(t, h, pinOut)
	rec := authedPost(t, h, cookie, "/api/projects", `{"repoUrl":"https://github.com/x/hello.git"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %q", rec.Code, rec.Body.String())
	}
	logs, _, _ := d.Obs.Read("x/hello", 0, 0, 200, "", "", "", "", "")
	var create, ready *obs.Entry
	for i := range logs {
		if logs[i].Type == "project.create" {
			create = &logs[i]
		}
		if logs[i].Type == "project.ready" {
			ready = &logs[i]
		}
	}
	if create == nil || ready == nil {
		t.Fatalf("logs = %+v, want create+ready", logs)
	}
	if create.Trace == "" || create.Trace != ready.Trace {
		t.Fatalf("traces differ: %q vs %q", create.Trace, ready.Trace)
	}
}
