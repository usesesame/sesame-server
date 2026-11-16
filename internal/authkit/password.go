package authkit

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 19 * 1024
	argonIterations  = 2
	argonParallelism = 1
	argonKeyLength   = 32
)

var (
	argonSlots = make(chan struct{}, argonConcurrencyLimit(runtime.NumCPU()))
	deriveKey  = argon2.IDKey
)

func argonConcurrencyLimit(cpus int) int {
	return min(max(cpus, 2), 4)
}

func deriveArgon2id(password, salt []byte, iterations, memory uint32, parallelism uint8) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return deriveKey(password, salt, iterations, memory, parallelism, argonKeyLength)
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := deriveArgon2id([]byte(password), salt, argonIterations, argonMemory, argonParallelism)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
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

func parseArgon2id(encoded string) (salt, expected []byte, iterations, memory uint32, parallelism uint8, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return nil, nil, 0, 0, 0, false
	}
	var parsedParallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parsedParallelism); err != nil {
		return nil, nil, 0, 0, 0, false
	}
	if memory == 0 || iterations == 0 || parsedParallelism == 0 {
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

func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return true
	}
	return parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonIterations, argonParallelism)
}
