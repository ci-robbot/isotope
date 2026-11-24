package installer

import (
	"strings"
	"time"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// stepWeights are the expected shares of the install's time each of Steps
// takes, by index: copying the active, recovery and passive images is
// nearly all of it. They are guesses from install logs, tuned like the
// matches in steps.go; a wrong weight gives a wrong estimate, never a wrong
// install.
var stepWeights = []float64{
	1,  // Preparing
	2,  // Partitioning disk
	1,  // Running before-install
	40, // Installing active system
	3,  // Configuring bootloader
	25, // Creating recovery image
	25, // Creating passive image
	2,  // Running after-install
	2,  // Checking the disk passphrase
	0,  // Complete
}

// ActiveStep is the index of "Installing active system" in Steps.
var ActiveStep = func() int {
	for i, s := range Steps {
		if strings.HasPrefix(s.Name, "Installing active") {
			return i
		}
	}
	return 0
}()

// EstimateMinDuration is how long the active system has to have been
// installing before there is an estimate: before that, the rate is noise.
const EstimateMinDuration = 30 * time.Second

// EstimateRemaining is how long the install has left, from the stage
// weights and the time spent: elapsed is the time since the install
// started, step the current step, and inActive how long since the active
// system started installing. The current step counts as half done. It
// reports false until the active system has been installing for
// EstimateMinDuration, and at the last step. encrypted says whether the
// passphrase check is part of this install.
func EstimateRemaining(step int, elapsed, inActive time.Duration, encrypted bool) (time.Duration, bool) {
	if step < ActiveStep || step >= len(Steps)-1 || inActive < EstimateMinDuration {
		return 0, false
	}
	var total, done float64
	for i, w := range stepWeights {
		if !encrypted && strings.Contains(Steps[i].Match, "checking the disk passphrase") {
			continue
		}
		total += w
		switch {
		case i < step:
			done += w
		case i == step:
			done += w / 2
		}
	}
	if done <= 0 || elapsed <= 0 {
		return 0, false
	}
	perWeight := float64(elapsed) / done
	left := time.Duration(perWeight * (total - done))
	return left, true
}

// RemainingText is the estimate in words: "About 3 minutes left", "About
// a minute left", "Less than a minute left".
func RemainingText(d time.Duration) string {
	m := int((d + 30*time.Second) / time.Minute)
	switch {
	case d < 45*time.Second:
		return i18n.T("Less than a minute left")
	case m <= 1:
		return i18n.T("About a minute left")
	}
	return i18n.Nf("About %d minute left", "About %d minutes left", m, m)
}
