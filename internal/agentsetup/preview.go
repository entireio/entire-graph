package agentsetup

import "fmt"

// Options is retained for compatibility with mirrored callers. Agent activation
// does not depend on plugin inventory or Brain runtime stores.
type Options struct {
	Mode                         Mode // empty inherits repository mode; explicit modes only affect this preview
	ListPlugins                  func() (string, error)
	StateDir, ConfigDir, DataDir string
}

// Preview shows the result of activating product without writing activation.
func Preview(repo, product string, opts Options) (string, error) {
	if product != "graph" && product != "brain" {
		return "", fmt.Errorf("unknown product %q", product)
	}
	if opts.Mode != "" && opts.Mode != ModeNormal && opts.Mode != ModeStrict {
		return "", fmt.Errorf("unknown guidance mode %q", opts.Mode)
	}
	if repo == "" {
		return guideFor(map[string]bool{product: true}, opts.Mode), nil
	}
	active, err := readActivation(repo)
	if err != nil {
		return "", err
	}
	active.products[product] = true
	if opts.Mode != "" {
		active.mode = opts.Mode
	}
	return renderActivation(active.products, active.mode), nil
}
