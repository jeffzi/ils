package guard

import (
	"context"
	"errors"

	"github.com/jeffzi/ils/internal/git"
)

// pushRepository returns the repository origin's first push URL names, the one git pushes to
// first, and false when that URL names no repository: a *git.OriginError, such as a local path or
// an ssh URL whose host starts with a dash, which git refuses to connect to. The fetch URL plays
// no part, so its failure is not returned. A failure to read the URLs from git, or to read the host
// of an ssh push URL, is an error.
func pushRepository(ctx context.Context, repo git.Repo) (git.Origin, bool, error) {
	urls, err := repo.OriginURLs(ctx)
	if len(urls.Push) == 0 {
		return git.Origin{}, false, err
	}

	push := urls.Push[0]
	if _, unnamed := errors.AsType[*git.OriginError](push.Err); unnamed {
		return git.Origin{}, false, nil
	}

	if push.Err != nil {
		return git.Origin{}, false, push.Err
	}

	return push.Origin, true, nil
}
