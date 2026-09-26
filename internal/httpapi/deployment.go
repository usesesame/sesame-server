package httpapi

import (
	"context"
	"errors"
	"net/http"
)

type DeploymentProfile string

const (
	DeploymentProfileOperator DeploymentProfile = "operator"
	DeploymentProfileProject  DeploymentProfile = "project"
)

func ParseDeploymentProfile(value string) (DeploymentProfile, error) {
	switch DeploymentProfile(value) {
	case "", DeploymentProfileOperator:
		return DeploymentProfileOperator, nil
	case DeploymentProfileProject:
		return DeploymentProfileProject, nil
	default:
		return DeploymentProfileOperator, errors.New("SESAME_DEPLOYMENT_PROFILE must be operator or project")
	}
}

const (
	releaseArtifactsUnavailableCode    = "release_artifacts_unavailable"
	releaseArtifactsUnavailableMessage = "This operator deployment does not serve Sesame release or download artifacts."
)

func (a *api) projectProfile() bool {
	return a.config.DeploymentProfile == DeploymentProfileProject
}

func (a *api) requireProjectArtifacts(response http.ResponseWriter) bool {
	if a.projectProfile() {
		return true
	}
	writeError(response, http.StatusServiceUnavailable, releaseArtifactsUnavailableCode, releaseArtifactsUnavailableMessage)
	return false
}

func (a *api) projectCapability(ctx context.Context, key string) bool {
	if !a.projectProfile() {
		return false
	}
	return a.capabilityEnabled(ctx, key)
}
