package cmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hashicorp/go-multierror"
	ictx "github.com/hashicorp/terraform-ls/internal/context"
	"github.com/hashicorp/terraform-ls/internal/filesystem"
	"github.com/hashicorp/terraform-ls/internal/logging"
	"github.com/hashicorp/terraform-ls/internal/state"
	"github.com/hashicorp/terraform-ls/internal/terraform/datadir"
	"github.com/hashicorp/terraform-ls/internal/terraform/module"
	"github.com/mitchellh/cli"
)

type InspectModuleCommand struct {
	Ui      cli.Ui
	Verbose bool

	logger *log.Logger
}

func (c *InspectModuleCommand) flags() *flag.FlagSet {
	fs := defaultFlagSet("debug")
	fs.BoolVar(&c.Verbose, "verbose", false, "whether to enable verbose output")
	fs.Usage = func() { c.Ui.Error(c.Help()) }
	return fs
}

func (c *InspectModuleCommand) Run(args []string) int {
	f := c.flags()
	if err := f.Parse(args); err != nil {
		c.Ui.Error(fmt.Sprintf("Error parsing command-line flags: %s", err))
		return 1
	}

	if f.NArg() != 1 {
		c.Ui.Output(fmt.Sprintf("expected exactly 1 argument (%d given): %q",
			f.NArg(), c.flags().Args()))
		return 1
	}

	path := f.Arg(0)

	var logDestination io.Writer
	if c.Verbose {
		logDestination = os.Stderr
	} else {
		logDestination = ioutil.Discard
	}

	c.logger = logging.NewLogger(logDestination)

	err := c.inspect(path)
	if err != nil {
		c.Ui.Output(err.Error())
		return 1
	}

	return 0
}

func (c *InspectModuleCommand) inspect(rootPath string) error {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}

	fi, err := os.Stat(rootPath)
	if err != nil {
		return err
	}

	if !fi.IsDir() {
		return fmt.Errorf("expected %s to be a directory", rootPath)
	}

	fs := filesystem.NewFilesystem()

	ctx := context.Background()
	ss, err := state.NewStateStore()
	if err != nil {
		return err
	}
	modMgr := module.NewSyncModuleManager(ctx, fs, ss.Modules, ss.ProviderSchemas)
	modMgr.SetLogger(c.logger)

	walker := module.SyncWalker(fs, modMgr)
	walker.SetLogger(c.logger)

	ctx, cancel := ictx.WithSignalCancel(context.Background(),
		c.logger, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	walker.EnqueuePath(rootPath)
	err = walker.StartWalking(ctx)
	if err != nil {
		return err
	}

	modules, err := modMgr.ListModules()
	if err != nil {
		return err
	}
	c.Ui.Output(fmt.Sprintf("%d modules found in total at %s", len(modules), rootPath))
	for _, mod := range modules {
		errs := &multierror.Error{}

		if mod.TerraformVersionErr != nil {
			multierror.Append(errs, mod.TerraformVersionErr)
		}

		if mod.ProviderSchemaErr != nil {
			multierror.Append(errs, mod.ProviderSchemaErr)
		}

		if mod.ModManifestErr != nil {
			multierror.Append(errs, mod.ModManifestErr)
		}

		if mod.ModuleParsingErr != nil {
			multierror.Append(errs, mod.ModuleParsingErr)
		}

		if mod.VarsParsingErr != nil {
			multierror.Append(errs, mod.VarsParsingErr)
		}

		errs.ErrorFormat = formatErrors

		modules := formatModuleRecords(mod.ModManifest.Records)
		subModules := fmt.Sprintf("%d modules", len(modules))
		if len(modules) > 0 {
			subModules += "\n"
			for _, m := range modules {
				subModules += fmt.Sprintf("     - %s", m)
			}
		}

		c.Ui.Output(fmt.Sprintf(` - %s
   - %s
   - %s`, mod.Path, errs, subModules))
	}
	c.Ui.Output("")

	return nil
}

func formatErrors(errors []error) string {
	if len(errors) == 0 {
		return "0 errors"
	}

	out := fmt.Sprintf("%d errors:\n", len(errors))
	for _, err := range errors {
		out += fmt.Sprintf("     - %s\n", err)
	}
	return strings.TrimSpace(out)
}

func formatModuleRecords(mds []datadir.ModuleRecord) []string {
	out := make([]string, 0)
	for _, m := range mds {
		if m.IsRoot() {
			continue
		}
		if m.IsExternal() {
			out = append(out, "EXTERNAL(%s)", m.SourceAddr)
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s)", m.Dir, m.SourceAddr))
	}
	return out
}

func (c *InspectModuleCommand) Help() string {
	helpText := `
Usage: terraform-ls inspect-module [path]

` + c.Synopsis() + "\n\n" + helpForFlags(c.flags())
	return strings.TrimSpace(helpText)
}

func (c *InspectModuleCommand) Synopsis() string {
	return "Lists available debug items"
}
