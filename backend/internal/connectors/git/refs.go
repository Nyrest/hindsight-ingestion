package git

import (
	"context"
	"errors"
	"path"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/mod/semver"
)

type reference struct {
	name plumbing.ReferenceName
	hash plumbing.Hash
}

func resolveRef(ctx context.Context, access *access, cfg sourceConfig) (reference, error) {
	if cfg.RefType == "commit" {
		return reference{hash: plumbing.NewHash(cfg.Ref)}, nil
	}
	remote := gogit.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{internalURL}})
	refs, err := remote.ListContext(ctx, &gogit.ListOptions{Auth: access})
	if err != nil {
		return reference{}, err
	}
	byName := map[plumbing.ReferenceName]*plumbing.Reference{}
	for _, ref := range refs {
		byName[ref.Name()] = ref
	}
	if cfg.RefType == "latest_tag" {
		return latestTag(refs, cfg.TagPattern)
	}
	name := plumbing.NewBranchReferenceName(cfg.Ref)
	if cfg.RefType == "tag" {
		name = plumbing.NewTagReferenceName(cfg.Ref)
	}
	if cfg.Ref == "" {
		name = plumbing.HEAD
	}
	ref := byName[name]
	if ref == nil {
		return reference{}, errors.New("selected Git reference does not exist")
	}
	if ref.Type() == plumbing.SymbolicReference {
		ref = byName[ref.Target()]
		if ref == nil {
			return reference{}, errors.New("default Git branch could not be resolved")
		}
	}
	return reference{name: ref.Name(), hash: ref.Hash()}, nil
}

func latestTag(refs []*plumbing.Reference, pattern string) (reference, error) {
	var selected *plumbing.Reference
	best := ""
	for _, ref := range refs {
		if !ref.Name().IsTag() {
			continue
		}
		name := ref.Name().Short()
		matched, _ := path.Match(pattern, name)
		version := name
		if len(version) > 0 && version[0] != 'v' {
			version = "v" + version
		}
		if !matched || !semver.IsValid(version) {
			continue
		}
		if selected == nil || semver.Compare(version, best) > 0 || (semver.Compare(version, best) == 0 && name > selected.Name().Short()) {
			selected = ref
			best = version
		}
	}
	if selected == nil {
		return reference{}, errors.New("no semantic version tags match the selected pattern")
	}
	return reference{name: selected.Name(), hash: selected.Hash()}, nil
}

func fetchCommit(ctx context.Context, cache *repositoryCache, access *access, ref reference) (*object.Commit, error) {
	source := string(ref.name)
	if source == "" {
		source = ref.hash.String()
	}
	destination := plumbing.ReferenceName("refs/ingestion/snapshot")
	err := cache.repo.FetchContext(ctx, &gogit.FetchOptions{RemoteName: "origin", RemoteURL: internalURL, Auth: access, Depth: 1, Tags: gogit.NoTags, Force: true, RefSpecs: []config.RefSpec{config.RefSpec("+" + source + ":" + string(destination))}})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil, err
	}
	fetched, err := cache.repo.Reference(destination, true)
	if err != nil {
		return nil, err
	}
	if ref.name == "" && fetched.Hash() != ref.hash {
		return nil, errors.New("Git server did not return the requested commit")
	}
	return peelCommit(ctx, cache.repo, fetched.Hash())
}

func peelCommit(ctx context.Context, repo *gogit.Repository, hash plumbing.Hash) (*object.Commit, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		commit, err := repo.CommitObject(hash)
		if err == nil {
			return commit, nil
		}
		tag, tagErr := repo.TagObject(hash)
		if tagErr != nil {
			return nil, err
		}
		hash = tag.Target
	}
}

func emptyRemote(err error, cfg sourceConfig) bool {
	return errors.Is(err, transport.ErrEmptyRemoteRepository) && cfg.RefType == "branch" && cfg.Ref == ""
}
