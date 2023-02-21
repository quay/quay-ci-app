package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-github/v42/github"
)

type dummyReactor struct {
	events []string
}

func (r *dummyReactor) HandleBranchPush(ctx context.Context, org, repo string, branch string) error {
	r.events = append(r.events, fmt.Sprintf("branch_push:%s/%s:%s", org, repo, branch))
	return nil
}

func (r *dummyReactor) HandleTagPush(ctx context.Context, org, repo string, tag string) error {
	r.events = append(r.events, fmt.Sprintf("tag_push:%s/%s:%s", org, repo, tag))
	return nil
}

func (r *dummyReactor) HandleCheckSuiteRerequest(ctx context.Context, org, repo string, suite *github.CheckSuite) error {
	var prs []string
	for _, pr := range suite.PullRequests {
		prs = append(prs, fmt.Sprintf("%d", pr.GetNumber()))
	}
	r.events = append(r.events, fmt.Sprintf("check_suite_rerequest:%s/%s:[%s]", org, repo, strings.Join(prs, ",")))
	return nil
}

func (r *dummyReactor) HandleIssueCommentCreate(ctx context.Context, org, repo string, issue *github.Issue, comment *github.IssueComment) error {
	r.events = append(r.events, fmt.Sprintf("issue_comment_create:%s/%s:%d:[%s]:[%s]", org, repo, issue.GetNumber(), issue.GetTitle(), comment.GetBody()))
	return nil
}

func (r *dummyReactor) HandlePullRequestClose(ctx context.Context, org, repo string, pr *github.PullRequest) error {
	r.events = append(r.events, fmt.Sprintf("pull_request_close:%s/%s:%d:[%s]", org, repo, pr.GetNumber(), pr.GetTitle()))
	return nil
}

func (r *dummyReactor) HandlePullRequestCreate(ctx context.Context, org, repo string, pr *github.PullRequest) error {
	r.events = append(r.events, fmt.Sprintf("pull_request_create:%s/%s:%d:[%s]", org, repo, pr.GetNumber(), pr.GetTitle()))
	return nil
}

func (r *dummyReactor) HandlePullRequestEdit(ctx context.Context, org, repo string, pr *github.PullRequest) error {
	r.events = append(r.events, fmt.Sprintf("pull_request_edit:%s/%s:%d:[%s]", org, repo, pr.GetNumber(), pr.GetTitle()))
	return nil
}

func TestPushEvent(t *testing.T) {
	const pushEvent = `{"ref":"refs/heads/master","before":"5a1fa17a799800f09a9bf447a5c83e3b01bd3ef1","after":"2219d5aed22f28546df28fac4a4c7d0cc783f9d6","repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("push", pushEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"branch_push:quay/quay:master"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestPushTagEvent(t *testing.T) {
	const pushEvent = `{"ref":"refs/tags/v3.8.0","before":"5a1fa17a799800f09a9bf447a5c83e3b01bd3ef1","after":"2219d5aed22f28546df28fac4a4c7d0cc783f9d6","repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("push", pushEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"tag_push:quay/quay:v3.8.0"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestCheckSuiteRerequest(t *testing.T) {
	const suiteEvent = `{"action":"rerequested","check_suite":{"pull_requests":[{"number":1}]},"repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("check_suite", suiteEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"check_suite_rerequest:quay/quay:[1]"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestPullRequestCommentRecheck(t *testing.T) {
	const commentEvent = `{"action":"created","issue":{"number":1,"title":"chore: Test PR (PROJQUAY-1234)","state":"open","pull_request":{}},"comment":{"body":"/retest"},"repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("issue_comment", commentEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"issue_comment_create:quay/quay:1:[chore: Test PR (PROJQUAY-1234)]:[/retest]"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestPullRequestMerged(t *testing.T) {
	const prEvent = `{"action":"closed","pull_request":{"number":1,"title":"chore: Test PR (PROJQUAY-1234)","state":"closed"},"repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("pull_request", prEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"pull_request_close:quay/quay:1:[chore: Test PR (PROJQUAY-1234)]"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestPullRequestCreate(t *testing.T) {
	const prEvent = `{"action":"opened","pull_request":{"number":1,"title":"chore: Test PR (PROJQUAY-1234)","state":"open"},"repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("pull_request", prEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"pull_request_create:quay/quay:1:[chore: Test PR (PROJQUAY-1234)]"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}

func TestPullRequestEdit(t *testing.T) {
	const prEvent = `{"action":"edited","pull_request":{"number":1,"title":"chore: Test PR (PROJQUAY-1234)","state":"open"},"repository":{"name":"quay","full_name":"quay/quay","private":false,"owner":{"name":"quay","login":"quay"}}}`

	r := &dummyReactor{}
	eh := &EventHandler{
		reactor: r,
	}
	err := eh.HandleEvent("pull_request", prEvent)
	if err != nil {
		t.Errorf("unexpected error: %s", err)
	}
	if !reflect.DeepEqual(r.events, []string{"pull_request_edit:quay/quay:1:[chore: Test PR (PROJQUAY-1234)]"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}
