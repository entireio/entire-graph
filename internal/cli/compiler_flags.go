package cli

import (
	"errors"

	"github.com/entireio/entire-graph/internal/compiler"
	"github.com/entireio/entire-graph/internal/sem"
)

type compilerFlags struct {
	Mode                              string
	Require                           bool
	Server, Hash, Toolchain, Launcher string
}

func (flags *compilerFlags) parse(args []string, index int) (int, error) {
	if args[index] == "--require-compiler" {
		flags.Require = true
		return index, nil
	}
	value, next, err := searchFlagValue(args, index)
	if err != nil {
		return index, err
	}
	switch args[index] {
	case "--compiler":
		if value != "off" && value != "go" {
			return index, errors.New("--compiler must be off or go")
		}
		flags.Mode = value
	case "--gopls":
		flags.Server = value
	case "--gopls-sha256":
		flags.Hash = value
	case "--go-toolchain":
		flags.Toolchain = value
	case "--compiler-launcher":
		flags.Launcher = value
	}
	return next, nil
}
func (flags compilerFlags) options() (*sem.CompilerOptions, error) {
	if flags.Mode == "" || flags.Mode == "off" {
		if flags.Require {
			return nil, errors.New("--require-compiler requires --compiler go")
		}
		return nil, nil
	}
	launcher := flags.Launcher
	if launcher == "" {
		launcher = "/usr/bin/bwrap"
	}
	return &sem.CompilerOptions{Config: compiler.Config{ServerPath: flags.Server, ServerSHA256: flags.Hash, ToolchainRoot: flags.Toolchain, BubblewrapPath: launcher}, Require: flags.Require}, nil
}
