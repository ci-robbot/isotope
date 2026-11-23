package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Latest is the newest published release, as GitHub reports it.
type Latest struct {
	Tag  string `json:"tag_name"`
	URL  string `json:"html_url"`
	Body string `json:"body"`
}

func apiBase() string {
	if u := os.Getenv("ISOTOPE_UPDATE_API"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.github.com"
}

// getJSON GETs a GitHub API path and decodes the JSON answer into out. The
// status handling lives here so every release query reports the same
// sentences for a missing repository and for the rate limit.
func getJSON(ctx context.Context, repo, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s", apiBase(), repo, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "isotope-update")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("%s has no published release yet", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return fmt.Errorf("GitHub rate limit reached, try again later (HTTP %d)", resp.StatusCode)
	default:
		return fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the release: %w", err)
	}
	return nil
}

// FetchLatest is the newest installable release (see FetchReleases). It is
// not GitHub's releases/latest: that is the release published last, and a
// release can be published, from the web page with the tag, an hour before
// release.yml has built and uploaded anything for it.
func FetchLatest(ctx context.Context, repo string, trustedBoot bool) (Latest, error) {
	rels, err := FetchReleases(ctx, repo, trustedBoot)
	if err != nil {
		return Latest{}, err
	}
	if len(rels) == 0 {
		return Latest{}, fmt.Errorf("%s has no release with images yet", repo)
	}
	return rels[0], nil
}

// releaseAsset is one file attached to a release. state is "uploaded" once
// the file is complete; GitHub lists an unfinished upload as "open".
type releaseAsset struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// hasManifest reports whether release.yml has finished with this release.
// Its release job uploads the ISOs and their .build.json manifests only after
// it has checked that every flavor's upgrade image is public, so one uploaded
// manifest means every image is there. A release without one is still being
// built, or its build failed: offering it would send the user to an image
// that does not exist.
//
// On a Trusted Boot system only a uki manifest counts
// (isotope-<flavor>-uki-<tag>-amd64.iso.build.json): a release built with
// TRUSTED_BOOT_RELEASE off has manifests but no <flavor>-uki-<tag> image.
func hasManifest(assets []releaseAsset, trustedBoot bool) bool {
	for _, a := range assets {
		if trustedBoot && !strings.Contains(a.Name, "-"+lineUKI+"-") {
			continue
		}
		if strings.HasSuffix(a.Name, ".build.json") && a.State == "uploaded" {
			return true
		}
	}
	return false
}

// FetchReleases lists the repository's installable releases, newest first,
// so the user can deliberately install an older one. Drafts, prereleases and
// releases with no uploaded manifest (see hasManifest; trustedBoot asks for
// a uki one) are dropped. One page of 100 is plenty for a picker.
func FetchReleases(ctx context.Context, repo string, trustedBoot bool) ([]Latest, error) {
	var raw []struct {
		Latest
		Draft      bool           `json:"draft"`
		Prerelease bool           `json:"prerelease"`
		Assets     []releaseAsset `json:"assets"`
	}
	if err := getJSON(ctx, repo, "releases?per_page=100", &raw); err != nil {
		return nil, err
	}
	var out []Latest
	for _, r := range raw {
		if r.Draft || r.Prerelease || r.Tag == "" || !hasManifest(r.Assets, trustedBoot) {
			continue
		}
		out = append(out, r.Latest)
	}
	// GitHub orders by creation date, which a backported patch release
	// breaks; order by version instead.
	sort.SliceStable(out, func(i, j int) bool { return Newer(out[i].Tag, out[j].Tag) })
	return out, nil
}
