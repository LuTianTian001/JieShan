package adminauth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	minimumPasswordBytes = 12
	maximumPasswordBytes = 1_024
)

type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltBytes   uint32
	KeyBytes    uint32
}

func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2,
		SaltBytes: 16, KeyBytes: 32,
	}
}

func normalizeArgon2Params(input Argon2Params) (Argon2Params, error) {
	if input == (Argon2Params{}) {
		input = DefaultArgon2Params()
	}
	if input.MemoryKiB < 8*1024 || input.MemoryKiB > 256*1024 {
		return Argon2Params{}, errors.New("Argon2id memory must be between 8192 and 262144 KiB")
	}
	if input.Iterations < 1 || input.Iterations > 10 {
		return Argon2Params{}, errors.New("Argon2id iterations must be between 1 and 10")
	}
	if input.Parallelism < 1 || input.Parallelism > 8 {
		return Argon2Params{}, errors.New("Argon2id parallelism must be between 1 and 8")
	}
	if input.SaltBytes < 16 || input.SaltBytes > 64 || input.KeyBytes < 16 || input.KeyBytes > 64 {
		return Argon2Params{}, errors.New("Argon2id salt and key lengths must be between 16 and 64 bytes")
	}
	return input, nil
}

func validateInitialPassword(password string) error {
	if len(password) < minimumPasswordBytes {
		return fmt.Errorf("initial administrator password must contain at least %d bytes", minimumPasswordBytes)
	}
	if len(password) > maximumPasswordBytes {
		return fmt.Errorf("initial administrator password exceeds %d bytes", maximumPasswordBytes)
	}
	return nil
}

func hashPassword(password string, params Argon2Params, random io.Reader) (string, error) {
	if err := validateInitialPassword(password); err != nil {
		return "", err
	}
	params, err := normalizeArgon2Params(params)
	if err != nil {
		return "", err
	}
	salt := make([]byte, params.SaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate Argon2id salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		params.MemoryKiB, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("administrator password hash is malformed")
	}
	params, err := parseArgon2Parameters(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, errors.New("administrator password hash salt is malformed")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errors.New("administrator password hash key is malformed")
	}
	params.SaltBytes = uint32(len(salt))
	params.KeyBytes = uint32(len(want))
	if _, err := normalizeArgon2Params(params); err != nil {
		return false, fmt.Errorf("administrator password hash parameters are unsafe: %w", err)
	}
	if len(password) > maximumPasswordBytes {
		return false, nil
	}
	got := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseArgon2Parameters(raw string) (Argon2Params, error) {
	values := strings.Split(raw, ",")
	if len(values) != 3 {
		return Argon2Params{}, errors.New("administrator password hash parameters are malformed")
	}
	parsed := make(map[string]uint64, 3)
	for _, value := range values {
		pair := strings.SplitN(value, "=", 2)
		if len(pair) != 2 {
			return Argon2Params{}, errors.New("administrator password hash parameters are malformed")
		}
		number, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return Argon2Params{}, errors.New("administrator password hash parameters are malformed")
		}
		if _, duplicate := parsed[pair[0]]; duplicate {
			return Argon2Params{}, errors.New("administrator password hash parameters are duplicated")
		}
		parsed[pair[0]] = number
	}
	memory, hasMemory := parsed["m"]
	iterations, hasIterations := parsed["t"]
	parallelism, hasParallelism := parsed["p"]
	if !hasMemory || !hasIterations || !hasParallelism || parallelism > 255 {
		return Argon2Params{}, errors.New("administrator password hash parameters are incomplete")
	}
	return Argon2Params{MemoryKiB: uint32(memory), Iterations: uint32(iterations), Parallelism: uint8(parallelism)}, nil
}
