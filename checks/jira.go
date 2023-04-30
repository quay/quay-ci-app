package checks

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"time"

	"github.com/andygrunwald/go-jira"
	"github.com/google/go-github/v42/github"
	"github.com/quay/quay-ci-app/configuration"
	"github.com/quay/quay-ci-app/taginformer"
	"k8s.io/klog/v2"
)

type Event string

const (
	EventClosed  Event = "closed"
	EventEdited  Event = "edited"
	EventOpened  Event = "opened"
	EventSync    Event = "sync"
	EventRecheck Event = "recheck"
)

var titleJiraRegex = regexp.MustCompile(` \(([A-Z]+-[0-9]+)\)$`)

const internalErrorMarker = "<!-- quay-ci-app: jira internal error -->"

func contains(list []string, str string) bool {
	for _, v := range list {
		if v == str {
			return true
		}
	}
	return false
}

func matchCondition(event Event, issue *jira.Issue, pr *github.PullRequest, fixVersion string, cond configuration.JiraCondition) bool {
	if len(cond.Status) > 0 {
		if !contains(cond.Status, issue.Fields.Status.Name) {
			return false
		}
	}
	if cond.Merged != nil {
		merged := !pr.GetMergedAt().IsZero()
		if merged != *cond.Merged {
			return false
		}
	}
	if cond.HasFixVersion != nil {
		if fixVersion == "" {
			return false
		}
		hasFixVersion := false
		for _, v := range issue.Fields.FixVersions {
			if v.Name == fixVersion {
				hasFixVersion = true
				break
			}
		}
		if hasFixVersion != *cond.HasFixVersion {
			return false
		}
	}
	if len(cond.Event) != 0 && !contains(cond.Event, string(event)) {
		return false
	}
	return true
}

type Jira struct {
	githubClient    *github.Client
	appGithubClient *github.Client
	jiraClient      *jira.Client
	tagInformer     *taginformer.TagInformer

	cachedGithubUserLogin string
}

func NewJira(githubClient *github.Client, appGithubClient *github.Client, jiraClient *jira.Client, tagInformer *taginformer.TagInformer) *Jira {
	return &Jira{
		githubClient:    githubClient,
		appGithubClient: appGithubClient,
		jiraClient:      jiraClient,
		tagInformer:     tagInformer,
	}
}

func (c *Jira) githubUserLogin() (string, error) {
	if c.cachedGithubUserLogin == "" {
		app, _, err := c.appGithubClient.Apps.Get(context.Background(), "")
		if err != nil {
			return "", fmt.Errorf("failed to get current app: %w", err)
		}
		c.cachedGithubUserLogin = fmt.Sprintf("%s[bot]", app.GetSlug())
	}
	return c.cachedGithubUserLogin, nil
}

func (c *Jira) reportTitleResult(ctx context.Context, owner, repo, headSHA string, number int, conclusion string, output *github.CheckRunOutput) error {
	klog.V(4).Infof("reporting Pull Request Title result on %s/%s#%d: %s: %s", owner, repo, number, conclusion, output.GetTitle())

	checkRun, _, err := c.githubClient.Checks.CreateCheckRun(ctx, owner, repo, github.CreateCheckRunOptions{
		Name:       "Pull Request Title",
		HeadSHA:    headSHA,
		Status:     github.String("completed"),
		Conclusion: github.String(conclusion),
		Output:     output,
	})

	cleanupErr := c.deleteOldComments(ctx, owner, repo, number, checkRun.GetCompletedAt().Time, internalErrorMarker)
	if cleanupErr != nil {
		klog.V(2).Infof("failed to delete old comments on %s/%s#%d: %v", owner, repo, number, cleanupErr)
	}

	return err
}

func (c *Jira) deleteOldComments(ctx context.Context, owner, repo string, number int, createdBefore time.Time, marker string) error {
	userLogin, err := c.githubUserLogin()
	if err != nil {
		return err
	}

	comments, _, err := c.githubClient.Issues.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		return fmt.Errorf("failed to list comments on pull request %s/%s#%d: %w", owner, repo, number, err)
	}

	for _, comm := range comments {
		if comm.GetUser().GetLogin() == userLogin && comm.GetCreatedAt().Before(createdBefore) && strings.Contains(comm.GetBody(), marker) {
			_, err = c.githubClient.Issues.DeleteComment(ctx, owner, repo, comm.GetID())
			if err != nil {
				klog.V(2).Infof("failed to delete comment %s/%s#%d:%d: %v", owner, repo, number, comm.GetID(), err)
			}
		}
	}

	return nil
}

func (c *Jira) reportInternalError(ctx context.Context, owner, repo, headSHA string, number int, msg string) error {
	klog.V(4).Infof("reporting internal error on %s/%s#%d: %s", owner, repo, number, msg)

	_, _, _ = c.githubClient.Checks.CreateCheckRun(ctx, owner, repo, github.CreateCheckRunOptions{
		Name:    "Pull Request Title",
		HeadSHA: headSHA,
		Status:  github.String("queued"),
	})
	comment, _, err := c.githubClient.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{
		Body: github.String(msg + "\n" + internalErrorMarker + "\n"),
	})
	if err == nil {
		c.cachedGithubUserLogin = comment.GetUser().GetLogin()

		err = c.deleteOldComments(ctx, owner, repo, number, comment.GetCreatedAt(), internalErrorMarker)
		if err != nil {
			klog.V(2).Infof("failed to delete old comments on %s/%s#%d: %v", owner, repo, number, err)
		}
	}
	return err
}

func (c *Jira) transitionTo(ctx context.Context, issue *jira.Issue, desiredStatus string) error {
	klog.V(4).Infof("transitioning issue %s from %s to %s...", issue.Key, issue.Fields.Status.Name, desiredStatus)

	transitions, _, err := c.jiraClient.Issue.GetTransitions(issue.Key)
	if err != nil {
		return fmt.Errorf("failed to get transitions for issue %s: %w", issue.Key, err)
	}
	for _, transition := range transitions {
		if transition.To.Name == desiredStatus {
			_, err = c.jiraClient.Issue.DoTransitionWithContext(ctx, issue.Key, transition.ID)
			if err != nil {
				return fmt.Errorf("failed to transition issue %s with transition %s: %w", issue.Key, transition.Name, err)
			}
			break
		}
	}

	return nil
}

func (c *Jira) setFixVersion(ctx context.Context, issue *jira.Issue, fixVersion string) error {
	for _, version := range issue.Fields.FixVersions {
		if version.Name == fixVersion {
			return nil
		}
	}

	_, err := c.jiraClient.Issue.UpdateIssueWithContext(ctx, issue.Key, map[string]interface{}{
		"update": map[string]interface{}{
			"fixVersions": []map[string]interface{}{
				{
					"add": map[string]interface{}{
						"name": fixVersion,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to set fix version %s for issue %s: %w", fixVersion, issue.Key, err)
	}

	return nil
}

func (c *Jira) applyRule(ctx context.Context, issue *jira.Issue, pr *github.PullRequest, fixVersion string, rule configuration.JiraRule) error {
	if rule.SetFixVersion && fixVersion != "" {
		err := c.setFixVersion(ctx, issue, fixVersion)
		if err != nil {
			return err
		}
	}

	if rule.Comment != "" {
		commentTemplate, err := template.New("comment").Parse(rule.Comment)
		if err != nil {
			return fmt.Errorf("failed to parse comment template: %w", err)
		}
		var commentBuffer bytes.Buffer
		err = commentTemplate.Execute(&commentBuffer, struct {
			PullRequest *github.PullRequest
		}{
			PullRequest: pr,
		})
		if err != nil {
			return fmt.Errorf("failed to execute comment template: %w", err)
		}
		_, _, err = c.jiraClient.Issue.AddComment(issue.Key, &jira.Comment{
			Body: commentBuffer.String(),
		})
		if err != nil {
			return fmt.Errorf("failed to add comment to issue %s: %w", issue.Key, err)
		}
	}

	err := c.transitionTo(ctx, issue, rule.TransitionTo)
	if err != nil {
		return fmt.Errorf("failed to transition Jira issue %s to %s: %v", issue.Key, rule.TransitionTo, err)
	}

	return nil
}

func (c *Jira) Run(event Event, jiraConfig configuration.Jira, branchConfig configuration.Branch, pr *github.PullRequest) error {
	if jiraConfig.Key == "" {
		return nil
	}

	ctx := context.Background()
	owner := pr.GetBase().GetRepo().GetOwner().GetLogin()
	repo := pr.GetBase().GetRepo().GetName()
	headSHA := pr.GetHead().GetSHA()

	klog.V(4).Infof("checking pull request %s/%s#%d...", owner, repo, pr.GetNumber())

	matches := titleJiraRegex.FindStringSubmatch(pr.GetTitle())
	key := ""
	if len(matches) != 0 {
		key = matches[1]
	}
	if !strings.HasPrefix(key, jiraConfig.Key+"-") {
		summary := "This check is skipped because the pull request title does not have a Jira issue in the title.\n"
		if key != "" {
			summary = "This check is skipped because the Jira issue `" + key + "` is not from the " + jiraConfig.Key + " project.\n"
		}
		summary += "\nThe title should be in the format `Title (" + jiraConfig.Key + "-123)` and the Jira issue should be from the " + jiraConfig.Key + " project.\n"

		return c.reportTitleResult(ctx, owner, repo, headSHA, pr.GetNumber(), "success", &github.CheckRunOutput{
			Title:   github.String("Pull request does not have a Jira issue in the title"),
			Summary: github.String(summary),
		})
	}

	issue, resp, err := c.jiraClient.Issue.Get(key, nil)
	if err != nil {
		klog.V(2).Infof("checking pull request %s/%s#%d: failed to get Jira issue %s: %v", owner, repo, pr.GetNumber(), key, err)

		if resp == nil {
			return c.reportInternalError(ctx, owner, repo, headSHA, pr.GetNumber(), "The Jira server is not reachable. You can retry the check by commenting `/recheck` on the pull request.")
		}
		if resp.StatusCode != 404 {
			return c.reportInternalError(ctx, owner, repo, headSHA, pr.GetNumber(), fmt.Sprintf("The Jira request failed with status code %d. You can retry the check by commenting `/recheck` on the pull request.", resp.StatusCode))
		}

		return c.reportTitleResult(ctx, owner, repo, headSHA, pr.GetNumber(), "failure", &github.CheckRunOutput{
			Title:   github.String("Jira issue " + key + " does not exist"),
			Summary: github.String("The Jira issue `" + key + "` does not exist.\n"),
		})
	}

	err = c.reportTitleResult(ctx, owner, repo, headSHA, pr.GetNumber(), "success", &github.CheckRunOutput{
		Title:   github.String("Pull request title has a valid Jira issue"),
		Summary: github.String("The pull request title is valid and has a Jira issue.\n"),
	})
	if err != nil {
		return err
	}

	fixVersion := ""
	if branchConfig.Version != "" {
		bareFixVersion, err := c.tagInformer.NextVersion(owner, repo, branchConfig.Version)
		if err != nil {
			return fmt.Errorf("failed to get next version for %s/%s:%s: %w", owner, repo, branchConfig.Name, err)
		}
		fixVersion = jiraConfig.FixVersionPrefix + bareFixVersion
	}

	for _, rule := range jiraConfig.Rules {
		if matchCondition(event, issue, pr, fixVersion, rule.When) {
			err = c.applyRule(ctx, issue, pr, fixVersion, rule)
			if err != nil {
				klog.V(2).Infof("checking pull request %s/%s#%d: %v", owner, repo, pr.GetNumber(), err)
			}
			break
		}
	}

	return nil
}
