package installer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGithub(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alex.keys":
			_, _ = w.Write([]byte("ssh-ed25519 AAAA1\nssh-rsa AAAA2\necdsa-sha2-nistp256 AAAA3\n"))
		case "/nokeys.keys":
		case "/broken.keys":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := GithubKeysURL
	GithubKeysURL = srv.URL + "/%s.keys"
	t.Cleanup(func() { GithubKeysURL = old })
}

func TestCountGithubKeys(t *testing.T) {
	fakeGithub(t)
	ctx := context.Background()
	c := http.DefaultClient
	if n, err := CountGithubKeys(ctx, c, "alex"); n != 3 || err != nil {
		t.Fatalf("alex: %d %v", n, err)
	}
	if n, err := CountGithubKeys(ctx, c, "nokeys"); n != 0 || err != nil {
		t.Fatalf("nokeys: %d %v", n, err)
	}
	if _, err := CountGithubKeys(ctx, c, "nobody"); !errors.Is(err, ErrGithubNoUser) {
		t.Fatalf("nobody: %v", err)
	}
	if _, err := CountGithubKeys(ctx, c, "broken"); err == nil || errors.Is(err, ErrGithubNoUser) {
		t.Fatalf("broken: %v", err)
	}
	if _, err := CountGithubKeys(ctx, c, "-bad-"); err == nil {
		t.Fatal("an invalid name was looked up")
	}
}

type failingDoer struct{}

func (failingDoer) Do(*http.Request) (*http.Response, error) { return nil, errors.New("no route") }

func TestGithubKeysText(t *testing.T) {
	_, err := CountGithubKeys(context.Background(), failingDoer{}, "alex")
	if txt, ok := GithubKeysText(0, err); ok || !strings.HasPrefix(txt, "Could not reach GitHub") {
		t.Fatalf("%q %v", txt, ok)
	}
	if txt, ok := GithubKeysText(3, nil); !ok || txt != "3 keys found" {
		t.Fatalf("%q", txt)
	}
	if txt, ok := GithubKeysText(1, nil); !ok || txt != "1 key found" {
		t.Fatalf("%q", txt)
	}
	if txt, ok := GithubKeysText(0, nil); ok || !strings.HasPrefix(txt, "No keys") {
		t.Fatalf("%q", txt)
	}
	if txt, _ := GithubKeysText(0, ErrGithubNoUser); !strings.HasPrefix(txt, "No GitHub user") {
		t.Fatalf("%q", txt)
	}
}
