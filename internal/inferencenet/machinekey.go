package inferencenet

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// ValidateKey reports whether an API key is accepted by the inference
// gateway. GET /models is authenticated and free, so it's the cheap probe.
func ValidateKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the gateway rejected the key (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not validate the key (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// apiKeyNameMaxLength matches the relay's apiKeyNameSchema cap; the hostname
// absorbs it so the timestamp always survives (mirrors fast-cli).
const apiKeyNameMaxLength = 64

// createAPIKey mints a machine key named `whip-<host>-<timestamp>` scoped to
// the primary project, returning id and plaintext key (shown once).
func createAPIKey(ctx context.Context, token, teamID, projectID string) (id, key, name string, err error) {
	hostname, _ := os.Hostname()
	ts := time.Now().UTC().Format(time.RFC3339)
	budget := apiKeyNameMaxLength - len("whip--"+ts)
	if len(hostname) > budget {
		hostname = hostname[:budget]
	}
	name = "whip-" + hostname + "-" + ts

	input := map[string]any{
		"defaultProjectId": projectID,
		"name":             name,
		"teamId":           teamID,
		"scopes": []map[string]any{
			{"permissions": []string{"read", "write"}, "projectId": projectID},
		},
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := trpcMutate(ctx, token, teamID, "apiKey.create", input, &created); err != nil {
		return "", "", "", err
	}
	return created.ID, created.Key, name, nil
}

// archiveAPIKey disables a key (logout / rotation).
func archiveAPIKey(ctx context.Context, token, teamID, apiKeyID string) error {
	return trpcMutate(ctx, token, teamID, "apiKey.archive",
		map[string]any{"apiKeyId": apiKeyID, "teamId": teamID}, nil)
}

// CompleteLogin runs the device flow, then selects and persists the primary
// project. The returned Auth has session + identity + project but no machine
// key yet — see EnsureMachineKey.
func CompleteLogin(ctx context.Context, onCode func(verificationURL, userCode string)) (Auth, error) {
	sess, err := Login(ctx, onCode)
	if err != nil {
		return Auth{}, err
	}
	a := Auth{SessionToken: sess.Token, UserEmail: sess.Email, TeamID: sess.TeamID}
	if err := a.selectPrimaryProject(ctx); err != nil {
		return Auth{}, err
	}
	return a, nil
}

// selectPrimaryProject records the team's first project on the auth state.
func (a *Auth) selectPrimaryProject(ctx context.Context) error {
	projects, err := getProjects(ctx, a.SessionToken, a.TeamID)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		return errors.New("no project found for this account; finish signup in the dashboard, then run `whip auth inference-net login` again")
	}
	a.ProjectID, a.ProjectName = projects[0].ID, projects[0].Name
	return nil
}

// EnsureMachineKey returns the stored machine key, minting one when absent.
// It does not re-validate a stored key (the inference call itself is the
// check); Rotate forces a fresh key.
func (a *Auth) EnsureMachineKey(ctx context.Context) (string, error) {
	if a.MachineKey != "" {
		return a.MachineKey, nil
	}
	return a.mintMachineKey(ctx)
}

// Rotate archives the stored key (if any) and mints a replacement.
func (a *Auth) Rotate(ctx context.Context) (string, error) {
	if a.MachineKeyID != "" {
		_ = archiveAPIKey(ctx, a.SessionToken, a.TeamID, a.MachineKeyID) // best-effort
		a.MachineKeyID, a.MachineKey, a.MachineKeyName = "", "", ""
	}
	return a.mintMachineKey(ctx)
}

// ArchiveMachineKey disables the stored machine key (logout).
func (a *Auth) ArchiveMachineKey(ctx context.Context) error {
	if a.MachineKeyID == "" || a.SessionToken == "" || a.TeamID == "" {
		return nil
	}
	return archiveAPIKey(ctx, a.SessionToken, a.TeamID, a.MachineKeyID)
}

func (a *Auth) mintMachineKey(ctx context.Context) (string, error) {
	if a.SessionToken == "" || a.TeamID == "" || a.ProjectID == "" {
		return "", errors.New("run `whip auth inference-net login` first")
	}
	id, key, name, err := createAPIKey(ctx, a.SessionToken, a.TeamID, a.ProjectID)
	if err != nil {
		return "", err
	}
	a.MachineKeyID, a.MachineKey, a.MachineKeyName = id, key, name
	return key, nil
}
