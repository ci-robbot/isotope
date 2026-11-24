package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// GithubKeysURL is where a GitHub user's public SSH keys are, the file
// cloud-config's github:NAME import reads. The tests point it at a fake.
var GithubKeysURL = "https://github.com/%s.keys"

// ErrGithubNoUser is a name GitHub does not know.
var ErrGithubNoUser = errors.New("no GitHub user of that name")

// HTTPDoer is the part of *http.Client the key lookup uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// CountGithubKeys is how many public SSH keys the GitHub user name has, so
// the installer can say "3 keys found" before the install imports them.
// A name GitHub does not know is ErrGithubNoUser; any other failure means
// GitHub could not be asked. Neither stops the install: the keys are
// optional.
func CountGithubKeys(ctx context.Context, c HTTPDoer, name string) (int, error) {
	if err := ValidateGithub(name); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(GithubKeysURL, name), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return 0, ErrGithubNoUser
	case resp.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("github: %s", resp.Status)
	}
	n := 0
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "ssh-") || strings.HasPrefix(l, "ecdsa-") || strings.HasPrefix(l, "sk-") {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// GithubKeysText is the result of CountGithubKeys in the installer's words,
// and whether it is good news (keys found) or a warning.
func GithubKeysText(n int, err error) (string, bool) {
	switch {
	case errors.Is(err, ErrGithubNoUser):
		return i18n.T("No GitHub user of that name. You can go on without keys."), false
	case err != nil:
		return i18n.T("Could not reach GitHub to check the name. You can go on."), false
	case n == 0:
		return i18n.T("No keys for that name. You can go on without keys."), false
	}
	return i18n.Nf("%d key found", "%d keys found", n, n), true
}
