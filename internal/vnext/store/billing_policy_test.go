package store

import (
	"math"
	"testing"
)

func TestScaleNanoUSDUsesExactBasisPointRounding(t *testing.T) {
	tests := []struct {
		name        string
		value       int64
		multiplier  int
		reservation bool
		want        int64
	}{
		{name: "identity", value: 125, multiplier: 10_000, want: 125},
		{name: "free", value: 125, multiplier: 0, want: 0},
		{name: "reservation rounds up", value: 1, multiplier: 1, reservation: true, want: 1},
		{name: "settlement rounds down below half", value: 1, multiplier: 1, want: 0},
		{name: "settlement rounds half up", value: 1, multiplier: 5_000, want: 1},
		{name: "decimal multiplier", value: 8, multiplier: 12_500, want: 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := scaleNanoUSD(test.value, test.multiplier, test.reservation)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("scaleNanoUSD() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestScaleNanoUSDRejectsOverflowAndInvalidInputs(t *testing.T) {
	if _, err := scaleNanoUSD(-1, DefaultBillingMultiplierBPS, false); err == nil {
		t.Fatal("negative charge was accepted")
	}
	if _, err := scaleNanoUSD(1, MaxBillingMultiplierBPS+1, false); err == nil {
		t.Fatal("oversized multiplier was accepted")
	}
	if _, err := scaleNanoUSD(math.MaxInt64, MaxBillingMultiplierBPS, false); err == nil {
		t.Fatal("overflowing charge was accepted")
	}
}

func TestBillingMultiplierFromBPS(t *testing.T) {
	tests := map[int]string{
		0:          "0",
		10_000:     "1",
		12_500:     "1.25",
		10_001:     "1.0001",
		1_000_000:  "100",
		10_000_000: "1000",
	}
	for input, want := range tests {
		got, err := BillingMultiplierFromBPS(input)
		if err != nil {
			t.Fatalf("BillingMultiplierFromBPS(%d): %v", input, err)
		}
		if got != want {
			t.Fatalf("BillingMultiplierFromBPS(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestHourlyWindowStartUsesUTCEpochHour(t *testing.T) {
	const hour = int64(60 * 60 * 1000)
	got, err := hourlyWindowStart(7*hour + 123_456)
	if err != nil {
		t.Fatal(err)
	}
	if got != 7*hour {
		t.Fatalf("hourlyWindowStart() = %d, want %d", got, 7*hour)
	}
	if _, err := hourlyWindowStart(0); err == nil {
		t.Fatal("zero timestamp was accepted")
	}
}
