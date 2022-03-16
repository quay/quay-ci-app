package main

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type dummyReactor struct {
	events []string
}

func (r *dummyReactor) HandleBranchPush(ctx context.Context, org, repo string, branch string) error {
	r.events = append(r.events, fmt.Sprintf("push:%s/%s:%s", org, repo, branch))
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
	if !reflect.DeepEqual(r.events, []string{"push:quay/quay:master"}) {
		t.Errorf("unexpected events: %v", r.events)
	}
}
