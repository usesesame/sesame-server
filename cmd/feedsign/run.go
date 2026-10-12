package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"usesesame.app/backend/internal/selfhost/updates"
)

const (
	usage = `feedsign makes and checks signed update feeds for sesame-server.

Usage:
  feedsign keygen -out <key file> [-id <key id>]
  feedsign sign -key <key file> -sequence <n> [-previous <n>] -in <feed.json> -out <signed.json>
  feedsign verify -pub <key id>:<public key> <signed.json>

The private key file is written with mode 0600 and is never printed.
`
	defaultKeyID  = "sesame-update-1"
	maxInputBytes = 1 << 20

	maxSequenceStep = 1000
)

func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout)
	case "sign":
		err = sign(args[1:], stdout, now())
	case "verify":
		err = verify(args[1:], stdout, now())
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "feedsign: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "feedsign: %v\n", err)
		return 1
	}
	return 0
}

func flags(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func keygen(args []string, stdout io.Writer) error {
	set := flags("keygen")
	out := set.String("out", "", "file for the private key")
	id := set.String("id", defaultKeyID, "key id")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *out == "" || set.NArg() != 0 {
		return errors.New("keygen needs -out and takes no other arguments")
	}
	if !updates.ValidKeyID(*id) {
		return errors.New("the key id may hold letters, digits, dot, dash and underscore, up to 64 characters")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	line := *id + ":" + base64.RawURLEncoding.EncodeToString(private.Seed()) + "\n"
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("the key file cannot be created: %w", err)
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		_ = os.Remove(*out)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(*out)
		return err
	}
	if err := os.Chmod(*out, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Private key written to %s with mode 0600.\nPublic key to pin:\n%s\n", *out, updates.FormatKey(*id, public))
	return nil
}

func readKey(path string) (string, ed25519.PrivateKey, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("the key file cannot be read: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("the key file must not be a symbolic link")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("the key file cannot be read: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("the key file cannot be read: %w", err)
	}
	if !os.SameFile(linkInfo, info) {
		return "", nil, errors.New("the key file changed while it was being opened")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", nil, errors.New("the key file must be a regular file that only its owner can read, for example mode 0600")
	}
	content, err := readFrom(file, path)
	if err != nil {
		return "", nil, err
	}
	id, encoded, found := strings.Cut(strings.TrimSpace(string(content)), ":")
	seed, decodeErr := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if !found || !updates.ValidKeyID(id) || decodeErr != nil || len(seed) != ed25519.SeedSize {
		return "", nil, errors.New("the key file is not a key id, a colon and an unpadded base64url seed")
	}
	return id, ed25519.NewKeyFromSeed(seed), nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readFrom(file, path)
}

func readFrom(file io.Reader, path string) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxInputBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxInputBytes)
	}
	return content, nil
}

func sign(args []string, stdout io.Writer, now time.Time) error {
	set := flags("sign")
	keyPath := set.String("key", "", "private key file")
	sequence := set.Int64("sequence", 0, "feed sequence")
	previous := set.Int64("previous", 0, "highest sequence published so far")
	in := set.String("in", "", "payload JSON file")
	out := set.String("out", "", "signed feed file")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *in == "" || *out == "" || set.NArg() != 0 {
		return errors.New("sign needs -key, -sequence, -in and -out")
	}
	if *sequence < 1 || *sequence > updates.MaxSequence {
		return fmt.Errorf("-sequence must be a whole number from 1 to %d", updates.MaxSequence)
	}
	previousGiven := false
	set.Visit(func(visited *flag.Flag) {
		if visited.Name == "previous" {
			previousGiven = true
		}
	})
	if previousGiven {
		if *previous < 0 || *previous > updates.MaxSequence {
			return fmt.Errorf("-previous must be a whole number from 0 to %d", updates.MaxSequence)
		}
		if *sequence < *previous {
			return fmt.Errorf("-sequence %d is lower than the previous sequence %d, and servers refuse a lower number", *sequence, *previous)
		}
		if *sequence > *previous+maxSequenceStep {
			return fmt.Errorf("-sequence %d is more than %d above the previous sequence %d, and servers would refuse every lower number afterwards", *sequence, maxSequenceStep, *previous)
		}
	}
	keyID, private, err := readKey(*keyPath)
	if err != nil {
		return err
	}
	content, err := readBounded(*in)
	if err != nil {
		return fmt.Errorf("the payload file cannot be read: %w", err)
	}
	var payload updates.Payload
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("the payload file is not a feed payload: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("the payload file has data after the JSON object")
	}
	if payload.Sequence != 0 && payload.Sequence != *sequence {
		return fmt.Errorf("the payload says sequence %d but -sequence is %d", payload.Sequence, *sequence)
	}
	payload.Sequence = *sequence
	registry := updates.DefaultRegistry()
	if err := updates.ValidatePayload(payload, registry); err != nil {
		return err
	}
	if err := updates.CheckFresh(payload, now); err != nil {
		return fmt.Errorf("refusing to sign: %w", err)
	}
	signed, err := updates.Sign(payload, keyID, private)
	if err != nil {
		return err
	}
	public := private.Public().(ed25519.PublicKey)
	if _, err := (updates.Verifier{Keys: updates.Keys{keyID: public}, Registry: registry}).Verify(signed, now); err != nil {
		return fmt.Errorf("the signed feed does not verify: %w", err)
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("the signed feed cannot be created: %w", err)
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		_ = os.Remove(*out)
		return err
	}
	if _, err := file.Write(append(signed, '\n')); err != nil {
		_ = file.Close()
		_ = os.Remove(*out)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(*out)
		return err
	}
	fmt.Fprintf(stdout, "Signed sequence %d with key %s and wrote %s.\n", payload.Sequence, keyID, *out)
	return nil
}

func verify(args []string, stdout io.Writer, now time.Time) error {
	set := flags("verify")
	pub := set.String("pub", "", "pinned public keys as id:key, comma separated")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *pub == "" || set.NArg() != 1 {
		return errors.New("verify needs -pub and one signed feed file")
	}
	keys, err := updates.ParseKeys(*pub)
	if err != nil {
		return err
	}
	content, err := readBounded(set.Arg(0))
	if err != nil {
		return fmt.Errorf("the signed feed cannot be read: %w", err)
	}
	document, err := (updates.Verifier{Keys: keys, Registry: updates.DefaultRegistry()}).Verify(content, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Valid. Key %s, sequence %d, issued %s, expires %s.\n", document.Envelope.KeyID, document.Payload.Sequence, document.Payload.IssuedAt.UTC().Format(time.RFC3339), document.Payload.ExpiresAt.UTC().Format(time.RFC3339))
	for _, product := range document.Payload.Products {
		channels := make([]string, 0, len(product.Channels))
		for channel := range product.Channels {
			channels = append(channels, channel)
		}
		sort.Strings(channels)
		for _, channel := range channels {
			fmt.Fprintf(stdout, "%s %s %s\n", product.ID, channel, product.Channels[channel].Version)
		}
	}
	return nil
}
