package installer

import (
	"testing"
	"time"
)

func TestStepWeightsMatchSteps(t *testing.T) {
	if len(stepWeights) != len(Steps) {
		t.Fatalf("%d weights for %d steps", len(stepWeights), len(Steps))
	}
	if Steps[ActiveStep].Name != "Installing active system" {
		t.Fatalf("ActiveStep is %q", Steps[ActiveStep].Name)
	}
}

func TestEstimateRemaining(t *testing.T) {
	if _, ok := EstimateRemaining(ActiveStep, 2*time.Minute, 29*time.Second, false); ok {
		t.Fatal("an estimate before 30 s of the active system")
	}
	if _, ok := EstimateRemaining(ActiveStep-1, 2*time.Minute, time.Minute, false); ok {
		t.Fatal("an estimate before the active system")
	}
	if _, ok := EstimateRemaining(len(Steps)-1, 2*time.Minute, time.Minute, false); ok {
		t.Fatal("an estimate when complete")
	}
	d1, ok := EstimateRemaining(ActiveStep, time.Minute, 40*time.Second, false)
	if !ok || d1 <= 0 {
		t.Fatalf("%v %v", d1, ok)
	}
	// Later in the install, less is left for the same pace.
	d2, _ := EstimateRemaining(ActiveStep+3, 3*time.Minute, 2*time.Minute, false)
	if d2 >= d1*3 {
		t.Fatalf("later estimate %v vs %v", d2, d1)
	}
	// The weights without the passphrase check: 4 of 99 before the active
	// step, so in the middle of it 24 are done; a minute for 24 leaves
	// 75/24 minutes.
	want := time.Duration(float64(time.Minute) / 24 * 75)
	if diff := d1 - want; diff > time.Second || diff < -time.Second {
		t.Fatalf("estimate %v, want %v", d1, want)
	}
	enc, _ := EstimateRemaining(ActiveStep, time.Minute, 40*time.Second, true)
	if enc <= d1 {
		t.Fatalf("encrypted %v not longer than %v", enc, d1)
	}
}

func TestRemainingText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:               "Less than a minute left",
		70 * time.Second:               "About a minute left",
		3*time.Minute + 10*time.Second: "About 3 minutes left",
		3*time.Minute + 40*time.Second: "About 4 minutes left",
	} {
		if got := RemainingText(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}
