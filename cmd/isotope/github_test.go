package main

import (
	"context"
	"reflect"
	"testing"
)

func TestFetchReleasesSkipsDraftsAndPrereleases(t *testing.T) {
	fakeGitHubReleases(t, "v0.0.5", `[
		{"tag_name":"v0.0.5","html_url":"u5"},
		{"tag_name":"v0.0.6-rc.1","prerelease":true},
		{"tag_name":"v0.0.7","draft":true},
		{"tag_name":"v0.0.4","html_url":"u4"}
	]`)
	got, err := FetchReleases(context.Background(), "kairos-community/isotope", false)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range got {
		tags = append(tags, l.Tag)
	}
	if want := []string{"v0.0.5", "v0.0.4"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
}

func TestFetchReleasesSortsNewestFirst(t *testing.T) {
	fakeGitHubReleases(t, "v0.0.5", `[{"tag_name":"v0.0.4"},{"tag_name":"v0.0.10"},{"tag_name":"v0.0.5"}]`)
	got, err := FetchReleases(context.Background(), "kairos-community/isotope", false)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range got {
		tags = append(tags, l.Tag)
	}
	if want := []string{"v0.0.10", "v0.0.5", "v0.0.4"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
}

func TestFetchReleasesUnknownRepo(t *testing.T) {
	fakeGitHub(t, "v0.0.5")
	if _, err := FetchReleases(context.Background(), "nobody/nothing", false); err == nil {
		t.Fatal("want an error for a 404")
	}
}

// A release published from the web page before release.yml has uploaded its
// manifests has no image behind it yet: it must not be offered, and the
// newest finished release is the latest instead. A failed build (no assets
// at all) and an upload still in progress count the same.
func TestFetchReleasesSkipsReleasesWithoutManifests(t *testing.T) {
	fakeGitHubReleases(t, "", `[
		{"tag_name":"v0.0.19","assets":[]},
		{"tag_name":"v0.0.18","assets":[{"name":"isotope-sway-v0.0.18-amd64.iso.build.json","state":"open"}]},
		{"tag_name":"v0.0.17","html_url":"u17","assets":[
			{"name":"isotope-sway-v0.0.17-amd64.iso","state":"uploaded"},
			{"name":"isotope-sway-v0.0.17-amd64.iso.build.json","state":"uploaded"}]},
		{"tag_name":"v0.0.15","assets":[{"name":"isotope-sway-v0.0.15-amd64.iso","state":"uploaded"}]},
		{"tag_name":"v0.0.12"}
	]`)
	got, err := FetchReleases(context.Background(), "kairos-community/isotope", false)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, l := range got {
		tags = append(tags, l.Tag)
	}
	if want := []string{"v0.0.17", "v0.0.12"}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	latest, err := FetchLatest(context.Background(), "kairos-community/isotope", false)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Tag != "v0.0.17" || latest.URL != "u17" {
		t.Fatalf("latest = %+v, want v0.0.17", latest)
	}
}

func TestFetchLatestWithNothingInstallable(t *testing.T) {
	fakeGitHubReleases(t, "", `[{"tag_name":"v0.0.19","assets":[]}]`)
	if _, err := FetchLatest(context.Background(), "kairos-community/isotope", false); err == nil {
		t.Fatal("want an error when no release has images")
	}
}
