package configuration

import (
	"io/ioutil"

	"sigs.k8s.io/yaml"
)

type BranchReference struct {
	Owner  string `yaml:"owner"`
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
}

func (br BranchReference) String() string {
	return br.Owner + "/" + br.Repo + ":" + br.Branch
}

type Branch struct {
	Name     string          `json:"name"`
	SyncFrom BranchReference `json:"syncFrom"`
}

type Repository struct {
	Owner    string   `json:"owner"`
	Repo     string   `json:"repo"`
	Branches []Branch `json:"branches"`
}

type Configuration struct {
	Repositories []Repository `json:"repositories"`
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
	buf, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var cfg Configuration
	if err := yaml.Unmarshal(buf, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
