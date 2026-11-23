package main

import "github.com/kairos-community/isotope/pkg/isorelease"

// The release file and the release tags live in isorelease, which Isotope
// Settings imports too. These names keep the rest of this package as it
// was.

// Release is the identity of the running system.
type Release = isorelease.Release

const (
	gpuFull = isorelease.GPUFull
	gpuVM   = isorelease.GPUVM
	lineUKI = isorelease.LineUKI
)

func releaseFile() string { return isorelease.File() }

// LoadRelease reads /etc/isotope/release; see isorelease.Load.
func LoadRelease(path string) (Release, error) { return isorelease.Load(path) }

func readKV(path string) (map[string]string, error) { return isorelease.ReadKV(path) }

func lineTag(flavor, line, tag string) string { return isorelease.LineTag(flavor, line, tag) }

// Newer reports whether candidate is a later release than current.
func Newer(candidate, current string) bool { return isorelease.Newer(candidate, current) }

// isTag says whether s is a release tag like v1.2.3.
func isTag(s string) bool { return isorelease.IsTag(s) }
