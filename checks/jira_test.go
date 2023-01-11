package checks

import (
	"testing"
	"time"

	"github.com/andygrunwald/go-jira"
	"github.com/google/go-github/v42/github"
	"github.com/quay/quay-ci-app/configuration"
)

type issueData struct {
	key         string
	status      string
	fixVersions []string
}

func fakeIssue(d issueData) *jira.Issue {
	fixVersions := make([]*jira.FixVersion, len(d.fixVersions))
	for i, v := range d.fixVersions {
		fixVersions[i] = &jira.FixVersion{Name: v}
	}

	return &jira.Issue{
		Key: d.key,
		Fields: &jira.IssueFields{
			Status: &jira.Status{
				Name: d.status,
			},
			FixVersions: fixVersions,
		},
	}
}

type pullRequestData struct {
	mergedAt string
}

func fakePullRequest(d pullRequestData) *github.PullRequest {
	var t time.Time
	if d.mergedAt != "" {
		t, _ = time.Parse(time.RFC3339, d.mergedAt)
	}
	return &github.PullRequest{
		MergedAt: &t,
	}
}

func TestMatchCondition(t *testing.T) {
	trueVal := true

	testCases := []struct {
		name        string
		cond        configuration.JiraCondition
		event       Event
		issue       issueData
		pullRequest pullRequestData
		fixVersion  string
		want        bool
	}{
		{
			name: "condition matches event type",
			cond: configuration.JiraCondition{
				Event: []string{"closed"},
			},
			event: EventClosed,
			want:  true,
		},
		{
			name: "condition does not match event type",
			cond: configuration.JiraCondition{
				Event: []string{"closed"},
			},
			event: EventRecheck,
			want:  false,
		},
		{
			name:  "no condition for event type",
			cond:  configuration.JiraCondition{},
			event: EventRecheck,
			want:  true,
		},
		{
			name: "issue doesn't have fix version",
			cond: configuration.JiraCondition{
				HasFixVersion: &trueVal,
			},
			event: EventRecheck,
			issue: issueData{
				key:    "PROJQUAY-123",
				status: "In Progress",
			},
			fixVersion: "quay-v3.8.1",
			want:       false,
		},
		{
			name: "issue has fix version",
			cond: configuration.JiraCondition{
				HasFixVersion: &trueVal,
			},
			event: EventRecheck,
			issue: issueData{
				key:         "PROJQUAY-123",
				status:      "In Progress",
				fixVersions: []string{"quay-v3.8.1"},
			},
			fixVersion: "quay-v3.8.1",
			want:       true,
		},
		{
			name: "issue has different fix version",
			cond: configuration.JiraCondition{
				HasFixVersion: &trueVal,
			},
			event: EventRecheck,
			issue: issueData{
				key:         "PROJQUAY-123",
				status:      "In Progress",
				fixVersions: []string{"quay-v3.9.0"},
			},
			fixVersion: "quay-v3.8.1",
			want:       false,
		},
		{
			name: "pull request is merged",
			cond: configuration.JiraCondition{
				Merged: &trueVal,
			},
			event: EventRecheck,
			pullRequest: pullRequestData{
				mergedAt: "2022-01-11T15:10:11Z",
			},
			want: true,
		},
		{
			name: "pull request is not merged",
			cond: configuration.JiraCondition{
				Merged: &trueVal,
			},
			event: EventRecheck,
			pullRequest: pullRequestData{
				mergedAt: "",
			},
			want: false,
		},
	}
	for _, tc := range testCases {
		if got := matchCondition(tc.event, fakeIssue(tc.issue), fakePullRequest(tc.pullRequest), tc.fixVersion, tc.cond); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}
