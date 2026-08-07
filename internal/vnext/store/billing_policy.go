package store

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const (
	BillingMultiplierScaleBPS   = 10_000
	DefaultBillingMultiplierBPS = BillingMultiplierScaleBPS
	MaxBillingMultiplierBPS     = 10_000_000
	hourWindowMilliseconds      = int64(60 * 60 * 1000)
)

func validateBillingMultiplierBPS(value int) error {
	if value < 0 || value > MaxBillingMultiplierBPS {
		return errors.New("billing multiplier must be between 0x and 1000x")
	}
	return nil
}

// BillingMultiplierFromBPS converts the persisted exact basis-point value to a
// display-friendly decimal multiplier without using floating-point arithmetic.
// Keeping this conversion in the store package gives control-plane responses
// and accounting the same canonical representation.
func BillingMultiplierFromBPS(value int) (string, error) {
	if err := validateBillingMultiplierBPS(value); err != nil {
		return "", err
	}
	whole := value / BillingMultiplierScaleBPS
	fraction := value % BillingMultiplierScaleBPS
	if fraction == 0 {
		return fmt.Sprintf("%d", whole), nil
	}
	// Four decimal places are sufficient because the persisted unit is BPS.
	text := fmt.Sprintf("%d.%04d", whole, fraction)
	return strings.TrimRight(strings.TrimRight(text, "0"), "."), nil
}

// scaleNanoUSD applies an exact basis-point multiplier. Reservations round up
// so a matching final charge always fits; settlements round half-up to the
// nearest nano-USD. big.Int keeps operator-controlled multipliers overflow-safe.
func scaleNanoUSD(value int64, multiplierBPS int, reservation bool) (int64, error) {
	if value < 0 {
		return 0, errors.New("nano-USD value cannot be negative")
	}
	if err := validateBillingMultiplierBPS(multiplierBPS); err != nil {
		return 0, err
	}
	if value == 0 || multiplierBPS == 0 {
		return 0, nil
	}
	numerator := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(multiplierBPS)))
	if reservation {
		numerator.Add(numerator, big.NewInt(BillingMultiplierScaleBPS-1))
	} else {
		numerator.Add(numerator, big.NewInt(BillingMultiplierScaleBPS/2))
	}
	numerator.Quo(numerator, big.NewInt(BillingMultiplierScaleBPS))
	if !numerator.IsInt64() {
		return 0, errors.New("multiplied nano-USD value exceeds the supported range")
	}
	return numerator.Int64(), nil
}

func hourlyWindowStart(timestampMS int64) (int64, error) {
	if timestampMS <= 0 {
		return 0, errors.New("hourly quota timestamp is invalid")
	}
	return timestampMS - timestampMS%hourWindowMilliseconds, nil
}
