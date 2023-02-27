package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"text/template"

	"github.com/go-shana/core/errors"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const shanaBuildServiceBinaryName = "shana-build-service"

type cmdRunContext struct {
	PkgName      string
	ProjectRoot  string
	ShanaCorePkg string
	ServicePkgs  []string
	ModFile      *modfile.File
	WorkFile     *modfile.WorkFile
	UseLocalCore bool
}

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run server-proto [flags] -- [go build flags]",
	Short: "Run current microservice as a server",
	Long: `The 'shana run' command is to run current microservice as a local server.
It's designed to be a development tool, not for production.

The 'server-proto' specifies the server protocol used by the service.
Here is a list of supported server protocols:

  - httpjson: Shana-opinioned HTTP JSON server.

More server protocols will be supported in the future.

Flags after '--' will be passed to 'go build' command to build the service.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		defer errors.Handle(&err)

		projectRoot, pkgName, modFile, workFile := findGoModule()
		errors.Assert(projectRoot != "", pkgName != "")

		// List all possible sub packages and sort them by name.
		pkgs := listAllSubPackages(projectRoot)

		for i := range pkgs {
			pkgs[i] = strings.Replace(pkgs[i], projectRoot, pkgName, 1)
		}

		sort.Strings(pkgs)

		if len(pkgs) == 0 {
			errors.Throwf("Fail to find any Go package in current project.")
			return
		}

		// Crate a temp directory and generate files to run the service.
		serverType := args[0]
		tmpls := listRunTemplates(serverType)
		cacheDir := errors.Check1(os.MkdirTemp("", "shana-workspace-*"))

		// Make sure cache dir is removed when SIGINT is signaled.
		defer os.RemoveAll(cacheDir)
		signalChan := make(chan os.Signal, 2)
		signal.Notify(signalChan, os.Interrupt)
		defer close(signalChan)
		defer signal.Stop(signalChan)

		// Store command in an atomic pointer and share with the signal handler.
		var cmdPtr atomic.Pointer[exec.Cmd]
		var interrupted atomic.Bool
		runCommand := func(msg, name string, args ...string) (err error) {
			if interrupted.Load() {
				return
			}

			command := exec.Command(name, args...)
			cmdPtr.Store(command)
			command.Dir = cacheDir
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			err = command.Run()
			cmdPtr.Store(nil)
			fmt.Fprintln(os.Stderr, msg)
			return
		}

		// Handle SIGINT.
		go func() {
			for range signalChan {

				if interrupted.Load() {
					return
				}

				interrupted.Store(true)

				if ptr := cmdPtr.Load(); ptr != nil {
					ptr.Process.Signal(syscall.SIGTERM)
				}

				fmt.Fprintln(os.Stderr, "Caught SIGINT")
			}
		}()

		// Copy config file if exists.
		configFile := path.Join(projectRoot, shanaYAML)

		if isFileExists(configFile) {
			errors.Check(os.Link(configFile, path.Join(cacheDir, shanaYAML)))
		}

		// Generate template files.
		cmdContext := &cmdRunContext{
			PkgName:      pkgName,
			ProjectRoot:  projectRoot,
			ShanaCorePkg: shanaCorePackage,
			ServicePkgs:  pkgs,
			ModFile:      modFile,
			WorkFile:     workFile,
		}

		for _, tmpl := range tmpls {
			createFile(path.Join(cacheDir, tmpl.Name()), tmpl, cmdContext)
		}

		// Tidy the go.mod file.
		errors.Check(runCommand("Fail to tidy the go.mod file.", "go", "mod", "tidy"))

		// Build the service.
		goBuildArgs := []string{"build", "-o", shanaBuildServiceBinaryName}
		goBuildArgs = append(goBuildArgs, parseGoBuildFlags(args)...)
		errors.Check(runCommand("Fail to build the service.", "go", goBuildArgs...))

		// TODO: use log to replace fmt.
		fmt.Fprintln(os.Stderr, "Service is about to be launched. Press Ctrl+C to stop the service.")

		// Run the service.
		// Error is ignored because the service may be stopped by Ctrl+C.
		runCommand("Service is stopped.", "./"+shanaBuildServiceBinaryName)

		return
	},
}

func findGoModule() (projectRoot, pkgName string, modFile *modfile.File, workFile *modfile.WorkFile) {
	command := exec.Command("go", "env", "GOMOD")
	output := &bytes.Buffer{}
	command.Stdout = output
	errors.If(command.Run()).Throw(errors.New("Fail to find go.mod in current project."))

	goMod := strings.TrimSpace(output.String())

	if goMod == os.DevNull {
		errors.Throwf("fail to find go.mod in current project.")
		return
	}

	projectRoot = path.Dir(goMod)

	goModData := errors.Check1(os.ReadFile(goMod))
	originalModFile := errors.Check1(modfile.Parse(goMod, goModData, nil))
	pkgName = originalModFile.Module.Mod.Path

	var originalWorkFile *modfile.WorkFile
	goWork := path.Join(projectRoot, "go.work")

	if isFileExists(goWork) {
		goWorkData := errors.Check1(os.ReadFile(goWork))
		originalWorkFile = errors.Check1(modfile.ParseWork(goWork, goWorkData, nil))
	}

	// Find the require and replace related to github.com/go-shana/core in go.mod.
	modFile = &modfile.File{
		Module: &modfile.Module{
			Mod: module.Version{
				Path: "github.com/go-shana/shana-workspace/debug-server",
			},
		},
		Go: originalModFile.Go,
	}

	for _, require := range originalModFile.Require {
		if require.Mod.Path == shanaCorePackage {
			modFile.Require = append(modFile.Require, require)
		}
	}

	for _, replace := range originalModFile.Replace {
		if replace.Old.Path == shanaCorePackage {
			if replace.New.Version == "" {
				replace.New.Path = path.Clean(path.Join(projectRoot, replace.New.Path))
			}

			modFile.Replace = append(modFile.Replace, replace)
		}
	}

	// Add current package to the replace.
	modFile.Replace = append(modFile.Replace, &modfile.Replace{
		Old: module.Version{
			Path: pkgName,
		},
		New: module.Version{
			Path: projectRoot,
		},
	})

	// Find replace related to github.com/go-shana/core in go.work.
	workFile = &modfile.WorkFile{
		Go: modFile.Go,
		Use: []*modfile.Use{
			{Path: "."},
		},
	}

	if originalWorkFile != nil {
		workFile.Go = originalWorkFile.Go

		for _, replace := range originalWorkFile.Replace {
			if replace.Old.Path == shanaCorePackage {
				if replace.New.Version == "" {
					replace.New.Path = path.Clean(path.Join(projectRoot, replace.New.Path))
				}

				workFile.Replace = append(workFile.Replace, replace)
			}
		}
	}

	return
}

func listAllSubPackages(projectRoot string) (pkgs []string) {
	entries := errors.Check1(os.ReadDir(projectRoot))
	hasGoFile := false

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() {
			if name == "internal" {
				continue
			}

			pkgs = append(pkgs, listAllSubPackages(path.Join(projectRoot, name))...)
			continue
		}

		// Ignore test files.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		if strings.HasSuffix(name, ".go") {
			hasGoFile = true
		}
	}

	if hasGoFile {
		pkgs = append(pkgs, projectRoot)
	}

	return
}

func parseGoBuildFlags(args []string) []string {
	buildFlags := args
	idx := 0

	for idx < len(buildFlags) {
		if buildFlags[idx] == "--" {
			idx++
			break
		}

		idx++
	}

	if idx >= len(buildFlags) {
		return nil
	}

	buildFlags = buildFlags[idx:]
	result := make([]string, 0, len(buildFlags))

	// Filter out -o flag.
	for idx = 0; idx < len(buildFlags); idx++ {
		if buildFlags[idx] == "-o" {
			idx++
			continue
		}

		result = append(result, buildFlags[idx])
	}

	return result
}

func init() {
	rootCmd.AddCommand(runCmd)
}

func listRunTemplates(serverType string) []*template.Template {
	var (
		httpjsonMainTemplate = template.Must(template.New("main.go").Parse(`package main

import (
	"{{.ShanaCorePkg}}/config"
	"{{.ShanaCorePkg}}/launcher"
	"{{.ShanaCorePkg}}/rpc"
	"{{.ShanaCorePkg}}/rpc/httpjson"

{{range .ServicePkgs}}	_ "{{.}}"{{println}}{{end -}}
)

var serverConfig = config.New[httpjson.Config]("shana.httpjson")

func main() {
	launcher.Launch(func() rpc.Server {
		serverConfig.PkgPrefix = "{{.PkgName}}"
		return httpjson.NewServer(serverConfig)
	})
}
`))

		goModTemplate = template.Must(template.New("go.mod").Parse(`module {{.ModFile.Module.Mod.Path}}

go {{.ModFile.Go.Version}}

require (
{{range .ModFile.Require}}	{{.Mod.Path}} {{.Mod.Version}}{{println}}{{end -}}
)

replace (
{{range .ModFile.Replace}}	{{.Old.Path}} {{- .Old.Version}} => {{.New.Path}} {{- .New.Version}}{{println}}{{end -}}
)
`))

		goWorkTemplate = template.Must(template.New("go.work").Parse(`go {{.WorkFile.Go.Version}}

use (
{{range .WorkFile.Use}}	{{.Path}}{{println}}{{end -}}
)

replace (
{{range .WorkFile.Replace}}	{{.Old.Path}} {{- .Old.Version}} => {{.New.Path}} {{- .New.Version}}{{println}}{{end -}}
)
`))
	)

	tmpls := []*template.Template{
		goModTemplate,
		goWorkTemplate,
	}

	switch serverType {
	case "httpjson":
		tmpls = append(tmpls, httpjsonMainTemplate)

	default:
		errors.Throwf("unsupported server-proto '%v'", serverType)
	}

	return tmpls
}
