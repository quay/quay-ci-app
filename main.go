package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v42/github"
	"github.com/quay/quay-ci-app/configuration"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

const (
	appID          = 174635   // Quay CI
	installationID = 23518045 // quay organization
	mainBranch     = "master"
)

var (
	addr       = flag.String("addr", ":8080", "listen address")
	configFile = flag.String("config", "./config.yaml", "configuration file")
	privateKey = flag.String("private-key", "", "private key file for the GitHub application")
)

type BranchStatus struct {
	Branch             string    `json:"branch"`
	Status             string    `json:"status"`
	Message            string    `json:"message"`
	LastHeartbeatTime  time.Time `json:"lastHeartbeatTime"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type Status struct {
	Branches []BranchStatus `json:"branches"`
}

func (s Status) DeepCopy() Status {
	branches := make([]BranchStatus, len(s.Branches))
	for i, branch := range s.Branches {
		branches[i] = branch
	}
	return Status{
		Branches: branches,
	}
}

type StatusInformer struct {
	mutex  sync.Mutex
	status Status
}

func (si *StatusInformer) GetStatus() Status {
	si.mutex.Lock()
	defer si.mutex.Unlock()
	return si.status.DeepCopy()
}

func (si *StatusInformer) UpdateBranchStatus(branch, status, message string) {
	si.mutex.Lock()
	defer si.mutex.Unlock()

	now := time.Now().UTC()

	for i, branchStatus := range si.status.Branches {
		if branchStatus.Branch == branch {
			if branchStatus.Status != status || branchStatus.Message != message {
				si.status.Branches[i].Status = status
				si.status.Branches[i].Message = message
				si.status.Branches[i].LastTransitionTime = now
			}
			si.status.Branches[i].LastHeartbeatTime = now
			return
		}
	}
	si.status.Branches = append(si.status.Branches, BranchStatus{
		Branch:             branch,
		Status:             status,
		Message:            message,
		LastHeartbeatTime:  now,
		LastTransitionTime: now,
	})
}

type Reactor interface {
	HandleBranchPush(ctx context.Context, org, repo string, branch string) error
}

type reactor struct {
	client         *github.Client
	cfg            *configuration.Configuration
	statusInformer *StatusInformer
}

func (r reactor) sync(ctx context.Context, dest, src configuration.BranchReference) error {
	sourceRef, _, err := r.client.Git.GetRef(ctx, src.Owner, src.Repo, "heads/"+src.Branch)
	if err != nil {
		err = fmt.Errorf("failed to get source ref: %w", err)
		r.statusInformer.UpdateBranchStatus(dest.String(), "SyncError", err.Error())
		return err
	}

	destinationRef, _, err := r.client.Git.GetRef(ctx, dest.Owner, dest.Repo, "heads/"+dest.Branch)
	if err != nil {
		err = fmt.Errorf("failed to get destination ref: %w", err)
		r.statusInformer.UpdateBranchStatus(dest.String(), "SyncError", err.Error())
		return err
	}

	klog.V(4).Infof("syching %s (%s) from %s (%s)...", src, sourceRef.Object.GetSHA(), dest, destinationRef.Object.GetSHA())

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
			r.statusInformer.UpdateBranchStatus(dest.String(), "SyncError", err.Error())
			return err
		}
	}

	r.statusInformer.UpdateBranchStatus(dest.String(), "Synced", fmt.Sprintf("synched from %s, commit: %s", src, sourceRef.Object.GetSHA()))

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

type EventHandler struct {
	reactor Reactor
}

func (eh *EventHandler) HandleEvent(eventType string, body string) error {
	switch eventType {
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

func main() {
	ctx := context.Background()
	tr := http.DefaultTransport

	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := configuration.LoadFromFile(*configFile)
	if err != nil {
		klog.Exitf("failed to load configuration: %v", err)
	}

	itr, err := ghinstallation.NewKeyFromFile(tr, appID, installationID, *privateKey)
	if err != nil {
		klog.Fatal(err)
	}

	client := github.NewClient(&http.Client{Transport: itr})
	statusInformer := &StatusInformer{}
	r := &reactor{
		client:         client,
		cfg:            cfg,
		statusInformer: statusInformer,
	}
	eh := &EventHandler{reactor: r}

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/status" {
				status := statusInformer.GetStatus()
				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(status)
				if err != nil {
					klog.Errorf("failed to encode status: %v", err)
				}
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				klog.Errorf("failed to read request body for %s %s from %s: %s", r.Method, r.URL.Path, r.RemoteAddr, err)
				return
			}
			if len(body) > 0 {
				contentType := r.Header.Get("Content-Type")
				event := r.Header.Get("X-GitHub-Event")
				klog.V(4).Infof("request from %s: %s %s: (content-type: %s, event: %s) %q", r.RemoteAddr, r.Method, r.URL, contentType, event, body)
				err := eh.HandleEvent(event, string(body))
				if err != nil {
					klog.Errorf("failed to handle event %s: %w", event, err)
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
					klog.Errorf("failed to sync %s: %s", syncTo, err)
				}
			}
		}

		time.Sleep(5 * time.Minute)
	}
}
