package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/ops"
	"usesesame.app/backend/internal/selfhost/secrets"
)

type recoverOptions struct {
	status        bool
	confirm       string
	retainOwners  []string
	retainDevices []string
	json          bool
}

type repeatedFlag []string

func (f *repeatedFlag) String() string { return strings.Join(*f, ",") }

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseRecoverArgs(args []string) (recoverOptions, error) {
	var options recoverOptions
	var owners, devices repeatedFlag
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.status, "status", false, "")
	flags.StringVar(&options.confirm, "confirm", "", "")
	flags.BoolVar(&options.json, "json", false, "")
	flags.Var(&owners, "retain-owner", "")
	flags.Var(&devices, "retain-device", "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, err
		}
		return options, fmt.Errorf("recover: %w", err)
	}
	if flags.NArg() > 0 {
		return options, fmt.Errorf("recover: takes no arguments but got %d", flags.NArg())
	}
	options.retainOwners = owners
	options.retainDevices = devices
	confirmSet := false
	flags.Visit(func(item *flag.Flag) {
		if item.Name == "confirm" {
			confirmSet = true
		}
	})
	if options.status && confirmSet {
		return options, errors.New("recover: --status and --confirm cannot be combined")
	}
	if !confirmSet {
		if len(options.retainOwners) > 0 || len(options.retainDevices) > 0 {
			return options, errors.New("recover: --retain-owner and --retain-device need --confirm")
		}
		options.status = true
		return options, nil
	}
	if !validRecoveryGeneration(options.confirm) {
		return options, errors.New("recover: --confirm needs a 32-character lowercase hexadecimal generation")
	}
	if err := checkUniqueIDs("owner", options.retainOwners); err != nil {
		return options, err
	}
	if err := checkUniqueIDs("device", options.retainDevices); err != nil {
		return options, err
	}
	return options, nil
}

func validRecoveryGeneration(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func checkUniqueIDs(kind string, ids []string) error {
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("recover: a retained %s ID is empty", kind)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("recover: %s %q is selected twice", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func runRecover(ctx context.Context, invocation Invocation) error {
	options, err := parseRecoverArgs(invocation.Args)
	if err != nil {
		return err
	}
	cfg := invocation.Config
	recovered, err := ops.Recover(cfg.DataDir)
	if errors.Is(err, ops.ErrInstanceRunning) {
		return fmt.Errorf("stop the running server before recovery: %w", err)
	}
	if err != nil {
		return fmt.Errorf("an interrupted restore in %s could not be finished: %w", cfg.DataDir, err)
	}
	if recovered != "" {
		fmt.Fprintf(invocation.Stderr, "An interrupted restore was finished first: %s\n", recovered)
	}
	present, err := databaseExists(databasePath(cfg))
	if err != nil {
		return err
	}
	if !present {
		return missingRecoveryDatabase(invocation, options)
	}
	lock, err := ops.AcquireInstanceLock(cfg.DataDir)
	if errors.Is(err, ops.ErrInstanceRunning) {
		return fmt.Errorf("stop the running server before recovery: %w", err)
	}
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			invocation.Logger.Warn("the instance lock could not be released cleanly", "error", err)
		}
	}()
	present, err = databaseExists(databasePath(cfg))
	if err != nil {
		return err
	}
	if !present {
		return missingRecoveryDatabase(invocation, options)
	}
	if options.status {
		return recoverStatus(ctx, invocation, options)
	}
	return recoverApproval(ctx, invocation, options)
}

func missingRecoveryDatabase(invocation Invocation, options recoverOptions) error {
	if options.status {
		status, owners, devices := emptyRecoveryStatus()
		return writeRecoveryStatus(invocation, options.json, status, owners, devices)
	}
	return fmt.Errorf("there is no database at %s", databasePath(invocation.Config))
}

func databaseExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func loadRecoverySecrets(invocation Invocation) (*secrets.Secrets, error) {
	loaded, err := secrets.Load(invocation.Config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("the instance secrets in %s cannot be read: %w", secrets.Dir(invocation.Config.DataDir), err)
	}
	return loaded, nil
}

func recoverStatus(ctx context.Context, invocation Invocation, options recoverOptions) error {
	loaded, err := loadRecoverySecrets(invocation)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, invocation.Config, loaded, storeReadOnly)
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := store.RecoveryStatus(ctx)
	if err != nil {
		return fmt.Errorf("%s recovery state cannot be read: %w", databasePath(invocation.Config), err)
	}
	owners, err := store.ListOwners(ctx)
	if err != nil {
		return err
	}
	devices, err := store.ListDevices(ctx, selfhost.DeviceFilter{IncludeInactive: true})
	if err != nil {
		return err
	}
	return writeRecoveryStatus(invocation, options.json, status, owners, devices)
}

func recoverApproval(ctx context.Context, invocation Invocation, options recoverOptions) error {
	loaded, err := loadRecoverySecrets(invocation)
	if err != nil {
		return err
	}
	preflight, err := openStore(ctx, invocation.Config, loaded, storeReadOnly)
	if err != nil {
		return err
	}
	status, statusErr := preflight.RecoveryStatus(ctx)
	check, checkErr := preflight.Check(ctx)
	closeErr := preflight.Close()
	if statusErr != nil {
		return fmt.Errorf("%s recovery state cannot be read: %w", databasePath(invocation.Config), statusErr)
	}
	if checkErr != nil {
		return fmt.Errorf("%s could not be checked before recovery approval: %w", databasePath(invocation.Config), checkErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%s could not be closed after the recovery preflight: %w", databasePath(invocation.Config), closeErr)
	}
	if !check.OK() {
		return fmt.Errorf("%s did not pass its checks, so recovery approval was refused", databasePath(invocation.Config))
	}
	if status.Pending && status.Generation != options.confirm {
		return fmt.Errorf("recovery generation %s does not match the pending restore; run sesame-server recover --status for the current generation", options.confirm)
	}
	if !status.Pending && status.Generation != options.confirm {
		return fmt.Errorf("there is no pending recovery for generation %s; run sesame-server recover --status", options.confirm)
	}
	if err := ops.VerifyLiveSecrets(ctx, invocation.Config.DataDir); err != nil {
		return fmt.Errorf("recovery approval was refused: %w", err)
	}
	store, err := openStore(ctx, invocation.Config, loaded, storeRecovery)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.CompleteRecovery(ctx, hostActor, options.confirm, selfhost.RecoverySelection{
		OwnerIDs:  options.retainOwners,
		DeviceIDs: options.retainDevices,
	})
	if err != nil {
		return recoveryApprovalError(options.confirm, err)
	}
	return writeRecoveryApproval(invocation, options, result)
}

func recoveryApprovalError(generation string, err error) error {
	switch {
	case errors.Is(err, selfhost.ErrRecoveryGenerationMismatch):
		return fmt.Errorf("recovery generation %s does not match the pending restore; run sesame-server recover --status for the current generation: %w", generation, err)
	case errors.Is(err, selfhost.ErrRecoveryOwnerRequired):
		return fmt.Errorf("at least one complete owner must be retained; pass --retain-owner with an owner ID from recover --status: %w", err)
	case errors.Is(err, selfhost.ErrRecoverySelectionInvalid):
		return fmt.Errorf("the selected owners and devices cannot be retained: %w", err)
	case errors.Is(err, selfhost.ErrRecoveryStateInvalid):
		return fmt.Errorf("the recovery state is invalid: %w", err)
	}
	return err
}

type recoveryOwnerJSON struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	SetupPending bool       `json:"setupPending"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
}

type recoveryHolderJSON struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type recoveryDeviceJSON struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Holder     recoveryHolderJSON `json:"holder"`
	ExpiresAt  time.Time          `json:"expiresAt"`
	LastSeenAt time.Time          `json:"lastSeenAt"`
	RevokedAt  *time.Time         `json:"revokedAt"`
}

type recoveryStatusJSON struct {
	Pending     bool                 `json:"pending"`
	Generation  string               `json:"generation"`
	StartedAt   *time.Time           `json:"startedAt"`
	CompletedAt *time.Time           `json:"completedAt"`
	Owners      []recoveryOwnerJSON  `json:"owners"`
	Devices     []recoveryDeviceJSON `json:"devices"`
}

type recoveryApprovalJSON struct {
	Pending         bool   `json:"pending"`
	Generation      string `json:"generation"`
	AlreadyComplete bool   `json:"alreadyComplete"`
	RetainedOwners  int    `json:"retainedOwners"`
	RemovedOwners   int    `json:"removedOwners"`
	RetainedDevices int    `json:"retainedDevices"`
	RevokedDevices  int    `json:"revokedDevices"`
}

func emptyRecoveryStatus() (selfhost.RecoveryStatus, []selfhost.Owner, []selfhost.Device) {
	return selfhost.RecoveryStatus{}, nil, nil
}

func writeRecoveryStatus(invocation Invocation, asJSON bool, state selfhost.RecoveryStatus, owners []selfhost.Owner, devices []selfhost.Device) error {
	if asJSON {
		return writeJSON(invocation.Stdout, recoveryStatusDocument(state, owners, devices))
	}
	if state.Pending {
		if _, err := fmt.Fprintf(invocation.Stdout, "Recovery is pending for generation %s, started %s.\n", state.Generation, formatTime(state.StartedAt)); err != nil {
			return err
		}
	} else if state.CompletedAt != nil {
		if _, err := fmt.Fprintf(invocation.Stdout, "Recovery generation %s completed at %s. No approval is pending.\n", state.Generation, formatTime(*state.CompletedAt)); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(invocation.Stdout, "No recovery approval is pending."); err != nil {
			return err
		}
		return nil
	}
	if !state.Pending {
		return nil
	}
	if len(owners) == 0 {
		if _, err := fmt.Fprintln(invocation.Stdout, "Owners: none."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(invocation.Stdout, "Owners:"); err != nil {
			return err
		}
		for _, owner := range owners {
			lastLogin := "never"
			if owner.LastLoginAt != nil {
				lastLogin = formatTime(*owner.LastLoginAt)
			}
			if _, err := fmt.Fprintf(invocation.Stdout, "  %q  %q  setup pending: %t  last login: %s\n", owner.ID, owner.Name, owner.SetupPending, lastLogin); err != nil {
				return err
			}
		}
	}
	if len(devices) == 0 {
		_, err := fmt.Fprintln(invocation.Stdout, "Devices: none.")
		return err
	}
	if _, err := fmt.Fprintln(invocation.Stdout, "Devices:"); err != nil {
		return err
	}
	for _, device := range devices {
		revoked := "no"
		if device.RevokedAt != nil {
			revoked = formatTime(*device.RevokedAt)
		}
		if _, err := fmt.Fprintf(invocation.Stdout, "  %q  %q  holder: %q:%q %q  expires: %s  last seen: %s  revoked: %s\n",
			device.ID, device.Name, device.Holder.Kind, device.Holder.ID, device.Holder.Name,
			formatTime(device.ExpiresAt), formatTime(device.LastSeenAt), revoked); err != nil {
			return err
		}
	}
	return nil
}

func recoveryStatusDocument(state selfhost.RecoveryStatus, owners []selfhost.Owner, devices []selfhost.Device) recoveryStatusJSON {
	document := recoveryStatusJSON{
		Pending:     state.Pending,
		Generation:  state.Generation,
		StartedAt:   optionalJSONTime(state.StartedAt),
		CompletedAt: state.CompletedAt,
		Owners:      []recoveryOwnerJSON{},
		Devices:     []recoveryDeviceJSON{},
	}
	for _, owner := range owners {
		document.Owners = append(document.Owners, recoveryOwnerJSON{
			ID:           owner.ID,
			Name:         owner.Name,
			SetupPending: owner.SetupPending,
			LastLoginAt:  owner.LastLoginAt,
		})
	}
	for _, device := range devices {
		document.Devices = append(document.Devices, recoveryDeviceJSON{
			ID:         device.ID,
			Name:       device.Name,
			Holder:     recoveryHolderJSON{Kind: string(device.Holder.Kind), ID: device.Holder.ID, Name: device.Holder.Name},
			ExpiresAt:  device.ExpiresAt,
			LastSeenAt: device.LastSeenAt,
			RevokedAt:  device.RevokedAt,
		})
	}
	return document
}

func writeRecoveryApproval(invocation Invocation, options recoverOptions, result selfhost.RecoveryResult) error {
	if options.json {
		return writeJSON(invocation.Stdout, recoveryApprovalJSON{
			Pending:         false,
			Generation:      options.confirm,
			AlreadyComplete: result.AlreadyComplete,
			RetainedOwners:  result.RetainedOwners,
			RemovedOwners:   result.RemovedOwners,
			RetainedDevices: result.RetainedDevices,
			RevokedDevices:  result.RevokedDevices,
		})
	}
	if result.AlreadyComplete {
		_, err := fmt.Fprintf(invocation.Stdout, "Recovery generation %s was already completed. No selection was applied.\n", options.confirm)
		return err
	}
	_, err := fmt.Fprintf(invocation.Stdout, "Recovery generation %s is complete. Retained %d owners and %d devices, removed %d owners and revoked %d devices.\n",
		options.confirm, result.RetainedOwners, result.RetainedDevices, result.RemovedOwners, result.RevokedDevices)
	return err
}

func writeJSON(w io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", encoded)
	return err
}

func optionalJSONTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.UTC().Format(time.RFC3339)
}
