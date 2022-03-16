package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v42/github"
	"k8s.io/klog/v2"
)

const (
	appID          = 174635   // Quay CI
	installationID = 23518045 // quay organization
	mainBranch     = "master"
)

var (
	releaseBranch = flag.String("release-branch", "redhat-3.7", "the branch to sync with the main one")
	privateKey    = flag.String("private-key", "", "private key file")
)

type Reactor interface {
	HandleBranchPush(ctx context.Context, org, repo string, branch string) error
}

type reactor struct {
	client *github.Client
}

func (r reactor) HandleBranchPush(ctx context.Context, org, repo string, branch string) error {
	if org != "quay" || repo != "quay" || branch != mainBranch {
		return nil
	}

	mainRef, _, err := r.client.Git.GetRef(ctx, org, repo, "heads/"+mainBranch)
	if err != nil {
		return err
	}

	releaseRef, _, err := r.client.Git.GetRef(ctx, "quay", "quay", "heads/"+*releaseBranch)
	if err != nil {
		return err
	}

	klog.V(4).Infof("%s ref: %s, %s ref: %s", mainBranch, mainRef.Object.GetSHA(), *releaseBranch, releaseRef.Object.GetSHA())

	if releaseRef.Object.GetSHA() != mainRef.Object.GetSHA() {
		klog.V(2).Infof("updating %s (%s -> %s)...", *releaseBranch, releaseRef.Object.GetSHA(), mainRef.Object.GetSHA())
		_, _, err := r.client.Git.UpdateRef(ctx, "quay", "quay", &github.Reference{
			Ref: github.String("heads/" + *releaseBranch),
			Object: &github.GitObject{
				SHA: mainRef.Object.SHA,
			},
		}, false)
		if err != nil {
			return fmt.Errorf("failed to update branch %s: %w", *releaseBranch, err)
		}
	}

	return nil
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

	itr, err := ghinstallation.NewKeyFromFile(tr, appID, installationID, *privateKey)
	if err != nil {
		klog.Fatal(err)
	}

	client := github.NewClient(&http.Client{Transport: itr})
	r := &reactor{client: client}
	eh := &EventHandler{reactor: r}

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
		if err := http.ListenAndServe(":8080", nil); err != nil {
			klog.Fatal(err)
		}
	}()

	for {
		err := r.HandleBranchPush(ctx, "quay", "quay", mainBranch)
		if err != nil {
			klog.Exit(err)
		}

		time.Sleep(2 * time.Minute)
	}
}
