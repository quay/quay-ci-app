package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andygrunwald/go-jira"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v42/github"
	"github.com/quay/quay-ci-app/checks"
	"github.com/quay/quay-ci-app/configuration"
	"golang.org/x/oauth2"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

var (
	addr          = flag.String("addr", ":8080", "listen address")
	configFile    = flag.String("config", "./config.yaml", "configuration file")
	jiraTokenFile = flag.String("jira-token", "./jira-token", "jira token file")
	jiraEndpoint  = flag.String("jira-endpoint", "https://issues.redhat.com", "jira endpoint")
	privateKey    = flag.String("private-key", "./private-key.pem", "private key file for the GitHub application")
)

var (
	refVersionRegex = regexp.MustCompile(`^refs/tags/v(\d+\.\d+)\.(\d+)$`)
	recheckRegex    = regexp.MustCompile(`(?mi)^/recheck\s*$`)
)

type BranchSyncStatus struct {
	Status             string    `json:"status"`
	Message            string    `json:"message"`
	LastHeartbeatTime  time.Time `json:"lastHeartbeatTime"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type BranchStatus struct {
	Branch     string            `json:"branch"`
	FixVersion string            `json:"fixVersion,omitempty"`
	SyncStatus *BranchSyncStatus `json:"syncStatus,omitempty"`
}

type Status struct {
	Branches []BranchStatus `json:"branches"`
}

func (s Status) DeepCopy() Status {
	branches := make([]BranchStatus, len(s.Branches))
	copy(branches, s.Branches)
	return Status{
		Branches: branches,
	}
}

func (s *Status) SetFixVersion(branch, fixVersion string) {
	for i := range s.Branches {
		branchStatus := &s.Branches[i]
		if branchStatus.Branch == branch {
			branchStatus.FixVersion = fixVersion
			return
		}
	}
	s.Branches = append(s.Branches, BranchStatus{
		Branch:     branch,
		FixVersion: fixVersion,
	})
}

type StatusInformer struct {
	mutex  sync.Mutex
	status Status
}

func (si *StatusInformer) statusSnapshot() Status {
	si.mutex.Lock()
	defer si.mutex.Unlock()
	return si.status.DeepCopy()
}

func (si *StatusInformer) GetStatus(cfg *configuration.Configuration, ti *TagInformer) Status {
	status := si.statusSnapshot()
	for _, repo := range cfg.Repositories {
		for _, branch := range repo.Branches {
			if branch.Version == "" {
				continue
			}
			fixVersion, err := ti.NextVersion(repo.Owner, repo.Repo, branch.Version)
			if err != nil {
				klog.Errorf("failed to get next version for %s/%s:%s: %v", repo.Owner, repo.Repo, branch.Version, err)
				continue
			}
			status.SetFixVersion(
				fmt.Sprintf("%s/%s:%s", repo.Owner, repo.Repo, branch.Name),
				repo.Jira.FixVersionPrefix+fixVersion,
			)
		}
	}
	return status
}

func (si *StatusInformer) UpdateBranchSyncStatus(branch, status, message string) {
	si.mutex.Lock()
	defer si.mutex.Unlock()

	now := time.Now().UTC()

	for i := range si.status.Branches {
		branchStatus := &si.status.Branches[i]
		if branchStatus.Branch == branch {
			if branchStatus.SyncStatus == nil {
				branchStatus.SyncStatus = &BranchSyncStatus{}
			}
			syncStatus := branchStatus.SyncStatus
			if syncStatus.Status != status || syncStatus.Message != message {
				syncStatus.Status = status
				syncStatus.Message = message
				syncStatus.LastTransitionTime = now
			}
			syncStatus.LastHeartbeatTime = now
			return
		}
	}
	si.status.Branches = append(si.status.Branches, BranchStatus{
		Branch: branch,
		SyncStatus: &BranchSyncStatus{
			Status:             status,
			Message:            message,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
		},
	})
}

type YStream struct {
	// patchVersions are sorted and unique.
	patchVersions []int
}

func (y *YStream) Add(z int) {
	i := sort.SearchInts(y.patchVersions, z)
	if i < len(y.patchVersions) && y.patchVersions[i] == z {
		// z is already in the list, do nothing
		return
	}

	y.patchVersions = append(y.patchVersions, 0)
	copy(y.patchVersions[i+1:], y.patchVersions[i:])
	y.patchVersions[i] = z
}

func (y *YStream) Next() int {
	if y == nil || len(y.patchVersions) == 0 {
		return 0
	}
	return y.patchVersions[len(y.patchVersions)-1] + 1
}

type TagInformer struct {
	mutex  sync.Mutex
	client *github.Client
	synced map[string]bool
	tags   map[string]*YStream
}

func (ti *TagInformer) key(org, repo, xy string) string {
	return fmt.Sprintf("%s/%s:%s", org, repo, xy)
}

func (ti *TagInformer) hasSynced(org, repo string) bool {
	ti.mutex.Lock()
	defer ti.mutex.Unlock()
	return ti.synced[fmt.Sprintf("%s/%s", org, repo)]
}

func (ti *TagInformer) addRefs(org, repo string, tags []*github.Reference) {
	ti.mutex.Lock()
	defer ti.mutex.Unlock()

	if ti.tags == nil {
		ti.tags = map[string]*YStream{}
	}

	for _, tag := range tags {
		match := refVersionRegex.FindStringSubmatch(tag.GetRef())
		if match != nil {
			xy := match[1]
			z := match[2]
			zInt, err := strconv.Atoi(z)
			if err != nil {
				// should never happen
				continue
			}
			key := ti.key(org, repo, xy)
			if ti.tags[key] == nil {
				ti.tags[key] = &YStream{}
			}
			ti.tags[key].Add(zInt)
		}
	}

	if ti.synced == nil {
		ti.synced = map[string]bool{}
	}
	ti.synced[fmt.Sprintf("%s/%s", org, repo)] = true
}

func (ti *TagInformer) init(org, repo string) error {
	klog.V(4).Infof("initializing tag informer for %s/%s", org, repo)

	tags, _, err := ti.client.Git.ListMatchingRefs(context.Background(), org, repo, &github.ReferenceListOptions{
		Ref: "tags/v",
	})
	if err != nil {
		return fmt.Errorf("failed to list tags: %w", err)
	}

	ti.addRefs(org, repo, tags)

	return nil
}

func (ti *TagInformer) NextVersion(org, repo, xy string) (string, error) {
	if !ti.hasSynced(org, repo) {
		if err := ti.init(org, repo); err != nil {
			return "", err
		}
	}

	ti.mutex.Lock()
	defer ti.mutex.Unlock()

	key := ti.key(org, repo, xy)
	z := ti.tags[key].Next()
	return fmt.Sprintf("%s.%d", xy, z), nil
}

type Reactor interface {
	HandleBranchPush(ctx context.Context, org, repo string, branch string) error
	HandleCheckSuiteRerequest(ctx context.Context, org, repo string, checkSuite *github.CheckSuite) error
	HandleIssueCommentCreate(ctx context.Context, org, repo string, issue *github.Issue, comment *github.IssueComment) error
	HandlePullRequestCreate(ctx context.Context, org, repo string, pr *github.PullRequest) error
	HandlePullRequestEdit(ctx context.Context, org, repo string, pr *github.PullRequest) error
}

type reactor struct {
	client         *github.Client
	cfg            *configuration.Configuration
	jiraCheck      *checks.Jira
	statusInformer *StatusInformer
}

func (r reactor) sync(ctx context.Context, dest, src configuration.BranchReference) error {
	sourceRef, _, err := r.client.Git.GetRef(ctx, src.Owner, src.Repo, "heads/"+src.Branch)
	if err != nil {
		err = fmt.Errorf("failed to get source ref: %w", err)
		r.statusInformer.UpdateBranchSyncStatus(dest.String(), "Error", err.Error())
		return err
	}

	destinationRef, _, err := r.client.Git.GetRef(ctx, dest.Owner, dest.Repo, "heads/"+dest.Branch)
	if err != nil {
		err = fmt.Errorf("failed to get destination ref: %w", err)
		r.statusInformer.UpdateBranchSyncStatus(dest.String(), "Error", err.Error())
		return err
	}

	klog.V(4).Infof("checking if %s (%s) is synced with %s (%s)...", dest, destinationRef.GetObject().GetSHA(), src, sourceRef.GetObject().GetSHA())

	if destinationRef.Object.GetSHA() != sourceRef.Object.GetSHA() {
		klog.V(2).Infof("updating %s (%s -> %s)...", dest, destinationRef.Object.GetSHA(), sourceRef.Object.GetSHA())
		_, _, err := r.client.Git.UpdateRef(ctx, dest.Owner, dest.Repo, &github.Reference{
			Ref: github.String("heads/" + dest.Branch),
			Object: &github.GitObject{
				SHA: sourceRef.Object.SHA,
			},
		}, false)
		if err != nil {
			err = fmt.Errorf("failed to update %s: %w", dest, err)
			r.statusInformer.UpdateBranchSyncStatus(dest.String(), "Error", err.Error())
			return err
		}
	}

	r.statusInformer.UpdateBranchSyncStatus(dest.String(), "Synced", fmt.Sprintf("synched from %s, commit: %s", src, sourceRef.Object.GetSHA()))

	return nil
}

func (r reactor) HandleBranchPush(ctx context.Context, org, repo string, branch string) error {
	from := configuration.BranchReference{
		Owner:  org,
		Repo:   repo,
		Branch: branch,
	}
	syncTo := r.cfg.BranchesSyncedFrom(org, repo, branch)
	var errs []error
	for _, to := range syncTo {
		err := r.sync(ctx, to, from)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.NewAggregate(errs)
}

func (r reactor) HandleCheckSuiteRerequest(ctx context.Context, org, repo string, checkSuite *github.CheckSuite) error {
	if checkSuite.GetApp().GetID() != r.cfg.AppID {
		return nil
	}

	for _, partialPR := range checkSuite.PullRequests {
		pr, _, err := r.client.PullRequests.Get(ctx, org, repo, partialPR.GetNumber())
		if err != nil {
			return fmt.Errorf("failed to get pull request: %w", err)
		}

		if err := r.jiraCheck.Run(checks.EventRecheck, r.cfg.Jira(org, repo), pr); err != nil {
			return fmt.Errorf("failed to run jira check: %w", err)
		}
	}

	return nil
}

func (r reactor) HandleIssueCommentCreate(ctx context.Context, org, repo string, issue *github.Issue, comment *github.IssueComment) error {
	if issue.GetState() != "open" {
		return nil
	}

	if issue.GetPullRequestLinks() == nil {
		return nil
	}

	if recheckRegex.MatchString(comment.GetBody()) {
		pr, _, err := r.client.PullRequests.Get(ctx, org, repo, issue.GetNumber())
		if err != nil {
			return fmt.Errorf("failed to get pull request: %w", err)
		}

		err = r.jiraCheck.Run(checks.EventRecheck, r.cfg.Jira(org, repo), pr)
		if err != nil {
			return fmt.Errorf("failed to run jira check: %w", err)
		}
	}

	return nil
}

func (r reactor) HandlePullRequestCreate(ctx context.Context, org, repo string, pr *github.PullRequest) error {
	return r.jiraCheck.Run(checks.EventOpened, r.cfg.Jira(org, repo), pr)
}

func (r reactor) HandlePullRequestEdit(ctx context.Context, org, repo string, pr *github.PullRequest) error {
	return r.jiraCheck.Run(checks.EventEdited, r.cfg.Jira(org, repo), pr)
}

type EventHandler struct {
	reactor Reactor
}

func (eh *EventHandler) HandleEvent(eventType string, body string) error {
	switch eventType {
	case "check_suite":
		var checkSuiteEvent github.CheckSuiteEvent
		err := json.Unmarshal([]byte(body), &checkSuiteEvent)
		if err != nil {
			return err
		}

		switch checkSuiteEvent.GetAction() {
		case "rerequested":
			return eh.reactor.HandleCheckSuiteRerequest(context.Background(), checkSuiteEvent.GetRepo().GetOwner().GetLogin(), checkSuiteEvent.GetRepo().GetName(), checkSuiteEvent.GetCheckSuite())
		}
	case "issue_comment":
		var issueCommentEvent github.IssueCommentEvent
		err := json.Unmarshal([]byte(body), &issueCommentEvent)
		if err != nil {
			return err
		}

		if issueCommentEvent.GetAction() == "created" {
			return eh.reactor.HandleIssueCommentCreate(context.Background(), issueCommentEvent.Repo.Owner.GetLogin(), issueCommentEvent.Repo.GetName(), issueCommentEvent.Issue, issueCommentEvent.Comment)
		}
	case "pull_request":
		var prEvent github.PullRequestEvent
		err := json.Unmarshal([]byte(body), &prEvent)
		if err != nil {
			return err
		}

		switch prEvent.GetAction() {
		case "opened":
			return eh.reactor.HandlePullRequestCreate(context.Background(), prEvent.Repo.Owner.GetLogin(), prEvent.Repo.GetName(), prEvent.PullRequest)
		case "edited":
			return eh.reactor.HandlePullRequestEdit(context.Background(), prEvent.Repo.Owner.GetLogin(), prEvent.Repo.GetName(), prEvent.PullRequest)
		}
	case "push":
		var pushEvent github.PushEvent
		err := json.Unmarshal([]byte(body), &pushEvent)
		if err != nil {
			return err
		}

		ref := pushEvent.GetRef()
		if strings.HasPrefix(ref, "refs/heads/") {
			branch := strings.TrimPrefix(ref, "refs/heads/")
			return eh.reactor.HandleBranchPush(context.Background(), pushEvent.Repo.Owner.GetLogin(), pushEvent.Repo.GetName(), branch)
		}
	}
	return nil
}

func newJiraClient(tokenFile string) (*jira.Client, error) {
	f, err := os.Open(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open jira token file: %w", err)
	}
	defer f.Close()

	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read jira token file: %w", err)
	}

	token := strings.TrimSpace(string(buf))

	tokenSource := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	return jira.NewClient(
		oauth2.NewClient(context.Background(), tokenSource),
		*jiraEndpoint,
	)
}

func main() {
	ctx := context.Background()
	tr := http.DefaultTransport

	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := configuration.LoadFromFile(*configFile)
	if err != nil {
		klog.Exitf("failed to load configuration: %v", err)
	}

	jiraClient, err := newJiraClient(*jiraTokenFile)
	if err != nil {
		klog.Exitf("failed to create jira client: %v", err)
	}

	itr, err := ghinstallation.NewKeyFromFile(tr, cfg.AppID, cfg.InstallationID, *privateKey)
	if err != nil {
		klog.Fatal(err)
	}

	apptr, err := ghinstallation.NewAppsTransportKeyFromFile(tr, cfg.AppID, *privateKey)
	if err != nil {
		klog.Fatal(err)
	}

	client := github.NewClient(&http.Client{Transport: itr})
	appClient := github.NewClient(&http.Client{Transport: apptr})
	tagInformer := &TagInformer{
		client: client,
	}
	statusInformer := &StatusInformer{}
	r := &reactor{
		client:         client,
		cfg:            cfg,
		jiraCheck:      checks.NewJira(client, appClient, jiraClient),
		statusInformer: statusInformer,
	}
	eh := &EventHandler{reactor: r}

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/status" {
				status := statusInformer.GetStatus(cfg, tagInformer)
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(status)
				if err != nil {
					klog.Errorf("failed to encode status: %v", err)
				}
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				klog.Errorf("failed to read request body for %s %s from %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
				return
			}
			if len(body) > 0 {
				contentType := r.Header.Get("Content-Type")
				event := r.Header.Get("X-GitHub-Event")
				if klog.V(6).Enabled() {
					klog.Infof("request from %s: %s %s: (content-type: %s, event: %s) %q", r.RemoteAddr, r.Method, r.URL, contentType, event, body)
				} else {
					klog.V(4).Infof("request from %s: %s %s: (content-type: %s, event: %s) [%d bytes]", r.RemoteAddr, r.Method, r.URL, contentType, event, len(body))
				}
				err := eh.HandleEvent(event, string(body))
				if err != nil {
					klog.Errorf("failed to handle event %s: %v", event, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			} else {
				klog.V(4).Infof("request from %s: %s %s", r.RemoteAddr, r.Method, r.URL)
				w.WriteHeader(http.StatusNotImplemented)
			}
		})
		if err := http.ListenAndServe(*addr, nil); err != nil {
			klog.Fatal(err)
		}
	}()

	for {
		for _, repo := range cfg.Repositories {
			for _, branch := range repo.Branches {
				syncFrom := branch.SyncFrom
				if syncFrom.Branch == "" {
					continue
				}
				if syncFrom.Owner == "" {
					syncFrom.Owner = repo.Owner
				}
				if syncFrom.Repo == "" {
					syncFrom.Repo = repo.Repo
				}
				syncTo := configuration.BranchReference{
					Owner:  repo.Owner,
					Repo:   repo.Repo,
					Branch: branch.Name,
				}
				err := r.sync(ctx, syncTo, syncFrom)
				if err != nil {
					klog.Errorf("failed to sync %s: %v", syncTo, err)
				}
			}
		}

		time.Sleep(5 * time.Minute)
	}
}
