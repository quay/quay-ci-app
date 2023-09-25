package configuration

import (
	"os"

	"sigs.k8s.io/yaml"
)

type JiraCondition struct {
	Status        []string `json:"status"`
	Merged        *bool    `json:"merged"`
	HasFixVersion *bool    `json:"has_fix_version"`
	Event         []string `json:"event"`
}

type JiraRule struct {
	TransitionTo  string        `json:"transition_to"`
	SetFixVersion bool          `json:"set_fix_version"`
	When          JiraCondition `json:"when"`
	Comment       string        `json:"comment"`
}

type Jira struct {
	Key              string     `json:"key"`
	FixVersionPrefix string     `json:"fix_version_prefix"`
	ValidIssueTypes  []string   `json:"valid_issue_types"`
	Rules            []JiraRule `json:"rules"`
}

type BranchReference struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

func (br BranchReference) String() string {
	return br.Owner + "/" + br.Repo + ":" + br.Branch
}

type Branch struct {
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	SyncFrom BranchReference `json:"sync_from"`
}

type Repository struct {
	Owner    string   `json:"owner"`
	Repo     string   `json:"repo"`
	Jira     Jira     `json:"jira"`
	Branches []Branch `json:"branches"`
}

type Configuration struct {
	AppID          int64        `json:"app_id"`
	InstallationID int64        `json:"installation_id"`
	Repositories   []Repository `json:"repositories"`
}

func (c *Configuration) Jira(owner, repoName string) Jira {
	for _, repo := range c.Repositories {
		if repo.Owner == owner && repo.Repo == repoName {
			return repo.Jira
		}
	}
	return Jira{}
}

func (c *Configuration) Branch(owner, repoName, branchName string) Branch {
	for _, repo := range c.Repositories {
		if repo.Owner == owner && repo.Repo == repoName {
			for _, branch := range repo.Branches {
				if branch.Name == branchName {
					return branch
				}
			}
		}
	}
	return Branch{
		Name: branchName,
	}
}

func (c *Configuration) BranchesSyncedFrom(owner, repoName, branchName string) []BranchReference {
	var refs []BranchReference
	for _, repo := range c.Repositories {
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
			if syncFrom.Owner == owner && syncFrom.Repo == repoName && syncFrom.Branch == branchName {
				refs = append(refs, BranchReference{
					Owner:  repo.Owner,
					Repo:   repo.Repo,
					Branch: branch.Name,
				})
			}
		}
	}
	return refs
}

func LoadFromFile(filename string) (*Configuration, error) {
	buf, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var cfg Configuration
	if err := yaml.Unmarshal(buf, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
