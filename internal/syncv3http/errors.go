package syncv3http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"usesesame.app/backend/internal/syncsvc"
)

const (
	codeInvalid          = "sync_invalid"
	codeSignatureInvalid = "sync_signature_invalid"
	codeForbidden        = "sync_forbidden"
	codeNotActivated     = "sync_not_activated"
	codeVaultNotFound    = "sync_vault_not_found"
	codeNotFound         = "sync_not_found"
	codeConflict         = "sync_conflict"
	codeReplayed         = "sync_replayed"
	codeVaultExists      = "sync_vault_exists"
	codeStorageLimit     = "sync_storage_limit_reached"
	codeUnavailable      = "sync_unavailable"
)

type errorSpec struct {
	status  int
	message string
}

var errorSpecs = map[string]errorSpec{
	codeInvalid:          {http.StatusBadRequest, "The sync request is invalid."},
	codeSignatureInvalid: {http.StatusUnauthorized, "The sync request signature is invalid."},
	codeForbidden:        {http.StatusForbidden, "This device is not allowed to do that."},
	codeNotActivated:     {http.StatusForbidden, "This device has not activated its vault key yet."},
	codeVaultNotFound:    {http.StatusNotFound, "This account has no synced vault."},
	codeNotFound:         {http.StatusNotFound, "The requested sync record was not found."},
	codeConflict:         {http.StatusConflict, "Sync state changed. Fetch it again and retry."},
	codeReplayed:         {http.StatusConflict, "This sync request was already used."},
	codeVaultExists:      {http.StatusConflict, "This account already has a synced vault."},
	codeStorageLimit:     {http.StatusRequestEntityTooLarge, "This vault has reached its Sync storage limit."},
	codeUnavailable:      {http.StatusServiceUnavailable, "Sesame Sync is temporarily unavailable."},
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func serviceErrorCode(err error) string {
	switch {
	case errors.Is(err, syncsvc.ErrSignature):
		return codeSignatureInvalid
	case errors.Is(err, syncsvc.ErrReplay):
		return codeReplayed
	case errors.Is(err, syncsvc.ErrNoVault):
		return codeVaultNotFound
	case errors.Is(err, syncsvc.ErrVaultExists):
		return codeVaultExists
	case errors.Is(err, syncsvc.ErrNotActivated):
		return codeNotActivated
	case errors.Is(err, syncsvc.ErrForbidden):
		return codeForbidden
	case errors.Is(err, syncsvc.ErrNotFound):
		return codeNotFound
	case errors.Is(err, syncsvc.ErrConflict):
		return codeConflict
	case errors.Is(err, syncsvc.ErrQuota):
		return codeStorageLimit
	case errors.Is(err, syncsvc.ErrInvalid):
		return codeInvalid
	default:
		return codeUnavailable
	}
}

func writeServiceError(response http.ResponseWriter, err error) {
	code := serviceErrorCode(err)
	if code == codeUnavailable {
		slog.Error("sync request failed", "error", err)
	}
	writeError(response, code)
}

func writeError(response http.ResponseWriter, code string) {
	spec, ok := errorSpecs[code]
	if !ok {
		code = codeUnavailable
		spec = errorSpecs[codeUnavailable]
	}
	writeJSON(response, spec.status, errorResponse{Error: errorBody{Code: code, Message: spec.message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("sync response encoding failed", "status", status, "error", err)
	}
}
