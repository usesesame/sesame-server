package authkit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 19 * 1024
	argonIterations  = 2
	argonParallelism = 1
	argonKeyLength   = 32
)

var ErrPasswordWorkBusy = errors.New("password work is busy")

var (
	argonSlots    = make(chan struct{}, argonConcurrencyLimit(runtime.NumCPU()))
	argonSlotWait = 2 * time.Second
	deriveKey     = argon2.IDKey
)

func argonConcurrencyLimit(cpus int) int {
	return min(max(cpus, 2), 4)
}

func deriveArgon2id(password, salt []byte, iterations, memory uint32, parallelism uint8) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return deriveKey(password, salt, iterations, memory, parallelism, argonKeyLength)
}

func acquireArgonSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrPasswordWorkBusy, err)
	}
	select {
	case argonSlots <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(argonSlotWait)
	defer timer.Stop()
	select {
	case argonSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-argonSlots
			return fmt.Errorf("%w: %w", ErrPasswordWorkBusy, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrPasswordWorkBusy, ctx.Err())
	case <-timer.C:
		return ErrPasswordWorkBusy
	}
}

func deriveArgon2idContext(ctx context.Context, password, salt []byte, iterations, memory uint32, parallelism uint8) ([]byte, error) {
	if err := acquireArgonSlot(ctx); err != nil {
		return nil, err
	}
	defer func() { <-argonSlots }()
	return deriveKey(password, salt, iterations, memory, parallelism, argonKeyLength), nil
}

func newArgonSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}

func encodeArgon2id(salt, hash []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}

func HashPassword(password string) (string, error) {
	salt, err := newArgonSalt()
	if err != nil {
		return "", err
	}
	hash := deriveArgon2id([]byte(password), salt, argonIterations, argonMemory, argonParallelism)
	return encodeArgon2id(salt, hash), nil
}

func HashPasswordContext(ctx context.Context, password string) (string, error) {
	salt, err := newArgonSalt()
	if err != nil {
		return "", err
	}
	hash, err := deriveArgon2idContext(ctx, []byte(password), salt, argonIterations, argonMemory, argonParallelism)
	if err != nil {
		return "", err
	}
	return encodeArgon2id(salt, hash), nil
}

func VerifyPassword(encoded, password string) bool {
	salt, expected, iterations, memory, parallelism, ok := parseArgon2id(encoded)
	if !ok {
		DummyVerifyPassword()
		return false
	}
	actual := deriveArgon2id([]byte(password), salt, iterations, memory, parallelism)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func VerifyPasswordContext(ctx context.Context, encoded, password string) (bool, error) {
	salt, expected, iterations, memory, parallelism, ok := parseArgon2id(encoded)
	if !ok {
		return false, DummyVerifyPasswordContext(ctx)
	}
	actual, err := deriveArgon2idContext(ctx, []byte(password), salt, iterations, memory, parallelism)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parseArgon2id(encoded string) (salt, expected []byte, iterations, memory uint32, parallelism uint8, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return nil, nil, 0, 0, 0, false
	}
	var parsedParallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parsedParallelism); err != nil {
		return nil, nil, 0, 0, 0, false
	}
	if memory == 0 || iterations == 0 || parsedParallelism == 0 || parsedParallelism > math.MaxUint8 {
		return nil, nil, 0, 0, 0, false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	expected, hashErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) != 16 || len(expected) != argonKeyLength {
		return nil, nil, 0, 0, 0, false
	}
	return salt, expected, iterations, memory, uint8(parsedParallelism), true
}

func DummyVerifyPassword() {
	dummySalt := make([]byte, 16)
	_, _ = rand.Read(dummySalt)
	_ = deriveArgon2id([]byte("dummy-password"), dummySalt, argonIterations, argonMemory, argonParallelism)
}

func DummyVerifyPasswordContext(ctx context.Context) error {
	dummySalt := make([]byte, 16)
	_, _ = rand.Read(dummySalt)
	_, err := deriveArgon2idContext(ctx, []byte("dummy-password"), dummySalt, argonIterations, argonMemory, argonParallelism)
	return err
}

func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return true
	}
	return parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonIterations, argonParallelism)
}
