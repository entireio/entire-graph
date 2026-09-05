package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type isolatedProcess struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	once   sync.Once
}

func (process *isolatedProcess) close() {
	process.once.Do(func() {
		_ = process.stdin.Close()
		_ = process.stdout.Close()
		if process.cmd.Cancel != nil {
			_ = process.cmd.Cancel()
		}
		_ = process.cmd.Wait()
	})
}
func validateConfig(config Config) error {
	if !filepath.IsAbs(config.ServerPath) || !filepath.IsAbs(config.ToolchainRoot) || !filepath.IsAbs(config.BubblewrapPath) || !validDigest(config.ServerSHA256) {
		return errors.New("compiler requires explicit absolute installed tool paths and pinned server SHA-256")
	}
	file, err := os.Open(config.ServerPath)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != config.ServerSHA256 {
		return errors.New("gopls executable digest does not match configured pin")
	}
	for _, tag := range config.Tags {
		if tag == "" || strings.ContainsAny(tag, " ,\t\r\n") {
			return errors.New("invalid compiler build tag")
		}
	}
	return nil
}
func launchIsolated(ctx context.Context, config Config, capsule string) (*isolatedProcess, error) {
	cmd, err := isolatedCommand(ctx, config, capsule, "/tools/gopls", "serve")
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	// Bounded protocol/diagnostic records carry failures; server logs cannot grow
	// a file or buffer under repository control.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	return &isolatedProcess{stdin: stdin, stdout: stdout, cmd: cmd}, nil
}

type limitedOutput struct {
	bytes []byte
	limit int
}

func (output *limitedOutput) Write(bytes []byte) (int, error) {
	if len(bytes) > output.limit-len(output.bytes) {
		return 0, errors.New("compiler discovery output limit")
	}
	output.bytes = append(output.bytes, bytes...)
	return len(bytes), nil
}

// Module discovery runs the approved Go executable in the SAME namespace as
// gopls. Every local module must be inside the immutable source capsule; missing
// external closure is explicit unavailability, never an ambient host read.
func discoverCapsule(ctx context.Context, config Config, capsule string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	cmd, err := isolatedCommand(ctx, config, capsule, "/toolchain/bin/go", "list", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	stdout, stderr := &limitedOutput{limit: MaxMessageBytes}, &limitedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("offline module closure unavailable: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(stdout.bytes)))
	var modules []string
	var patterns []string
	for {
		var module struct {
			Path    string
			Dir     string
			Replace *struct {
				Dir  string
				Path string
			}
		}
		if err := decoder.Decode(&module); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		directory := module.Dir
		if module.Replace != nil {
			directory = module.Replace.Dir
		}
		if directory != "/workspace" && !strings.HasPrefix(directory, "/workspace/") {
			return nil, errors.New("module dependency is not in captured source closure")
		}
		modules = append(modules, module.Path)
		patterns = append(patterns, module.Path+"/...")
		if len(modules) > 10000 {
			return nil, errors.New("compiler module limit")
		}
	}
	if len(modules) == 0 {
		return nil, errors.New("no captured Go module")
	}
	// Ask the approved toolchain for actual selected packages and all imports.
	// Discovery is still read-only/offline; package errors are coverage failures.
	arguments := []string{"list", "-e", "-deps", "-json"}
	if len(config.Tags) > 0 {
		arguments = append(arguments, "-tags="+strings.Join(config.Tags, ","))
	}
	arguments = append(arguments, patterns...)
	cmd, err = isolatedCommand(ctx, config, capsule, "/toolchain/bin/go", arguments...)
	if err != nil {
		return nil, err
	}
	stdout, stderr = &limitedOutput{limit: MaxMessageBytes}, &limitedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("offline package discovery unavailable: %w", err)
	}
	decoder = json.NewDecoder(strings.NewReader(string(stdout.bytes)))
	packages := map[string]bool{}
	for {
		var pkg struct {
			ImportPath, Dir string
			Standard        bool
			Error           json.RawMessage
			DepsErrors      []json.RawMessage
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if len(pkg.Error) > 0 && string(pkg.Error) != "null" || len(pkg.DepsErrors) > 0 {
			return nil, errors.New("captured package or dependency has build errors")
		}
		if pkg.Standard {
			continue
		}
		if pkg.Dir != "/workspace" && !strings.HasPrefix(pkg.Dir, "/workspace/") {
			return nil, errors.New("package dependency is not captured")
		}
		packages[pkg.ImportPath] = true
		if len(packages) > 10000 {
			return nil, errors.New("compiler package limit")
		}
	}
	result := make([]string, 0, len(packages))
	for name := range packages {
		result = append(result, name)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, errors.New("no selected Go packages")
	}
	return result, nil
}
