package taginformer

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"sync"

	"github.com/google/go-github/v42/github"
	"k8s.io/klog/v2"
)

var refVersionRegex = regexp.MustCompile(`^refs/tags/v(\d+\.\d+)\.(\d+)$`)

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

func (y *YStream) Remove(z int) {
	i := sort.SearchInts(y.patchVersions, z)
	if i < len(y.patchVersions) && y.patchVersions[i] == z {
		y.patchVersions = append(y.patchVersions[:i], y.patchVersions[i+1:]...)
	}
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

func New(client *github.Client) *TagInformer {
	return &TagInformer{
		client: client,
	}
}

func (ti *TagInformer) key(org, repo, xy string) string {
	return fmt.Sprintf("%s/%s:%s", org, repo, xy)
}

func (ti *TagInformer) hasSynced(org, repo string) bool {
	ti.mutex.Lock()
	defer ti.mutex.Unlock()
	return ti.synced[fmt.Sprintf("%s/%s", org, repo)]
}

func (ti *TagInformer) InvalidateCache() {
	ti.mutex.Lock()
	defer ti.mutex.Unlock()
	ti.synced = nil
	ti.tags = nil
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
