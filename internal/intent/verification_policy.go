package intent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// VerificationPolicyFile is repository-authored metadata for explicit verify scopes.
const VerificationPolicyFile = "verification.yaml"

// VerificationPolicy is metadata only. The CLI executes only commands supplied by its caller.
type VerificationPolicy struct {
	Version int                       `yaml:"version" json:"version"`
	Scopes  []VerificationPolicyScope `yaml:"scopes" json:"scopes"`
	Digest  string                    `yaml:"-" json:"digest"`
}

type VerificationPolicyScope struct {
	ID           string `yaml:"id" json:"id"`
	Command      string `yaml:"command" json:"command"`
	SetupCommand string `yaml:"setup_command,omitempty" json:"setup_command,omitempty"`
}

// LoadVerificationPolicy reads the optional local policy. An absent policy has a
// stable digest, so legacy evidence is never silently treated as comparable.
func LoadVerificationPolicy(repo string) (VerificationPolicy, error) {
	policy := VerificationPolicy{Version: 1, Scopes: []VerificationPolicyScope{}}
	path := filepath.Join(repo, Root, VerificationPolicyFile)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		policy.Digest = digest(nil)
		return policy, nil
	} else if err != nil {
		return policy, err
	}
	if err := decodeFile(path, &policy); err != nil {
		return policy, fmt.Errorf("read verification policy: %w", err)
	}
	if policy.Version != 1 {
		return policy, errors.New("verification policy must use version 1")
	}
	if len(policy.Scopes) == 0 {
		return policy, errors.New("verification policy requires at least one scope")
	}
	seen := make(map[string]bool, len(policy.Scopes))
	for _, scope := range policy.Scopes {
		if scope.ID != strings.TrimSpace(scope.ID) || scope.ID == "" || strings.TrimSpace(scope.Command) == "" || seen[scope.ID] {
			return policy, errors.New("verification policy scopes need unique ids and commands")
		}
		seen[scope.ID] = true
	}
	sort.Slice(policy.Scopes, func(i, j int) bool { return policy.Scopes[i].ID < policy.Scopes[j].ID })
	data, err := yaml.Marshal(struct {
		Version int                       `yaml:"version"`
		Scopes  []VerificationPolicyScope `yaml:"scopes"`
	}{Version: policy.Version, Scopes: policy.Scopes})
	if err != nil {
		return policy, err
	}
	policy.Digest = digest(data)
	return policy, nil
}

// VerifyScope compares caller-provided metadata to the selected declared scope.
// It never returns a command for the caller to execute.
func (policy VerificationPolicy) VerifyScope(scope, command, setup string) error {
	if len(policy.Scopes) == 0 {
		if scope != "" {
			return fmt.Errorf("verification scope %q is not declared by the local policy", scope)
		}
		return nil
	}
	if scope == "" {
		return errors.New("verification policy requires --scope")
	}
	for _, declared := range policy.Scopes {
		if declared.ID != scope {
			continue
		}
		if declared.Command != command || declared.SetupCommand != setup {
			return fmt.Errorf("verification scope %q command metadata does not match the local policy", scope)
		}
		return nil
	}
	return fmt.Errorf("verification scope %q is not declared by the local policy", scope)
}
