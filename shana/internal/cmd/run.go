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
)

const shanaBuildServiceBinaryName = "shana-build-service"

type cmdRunContext struct {
	PkgName      string
	ProjectRoot  string
	ShanaCorePkg string
	ServicePkgs  []string
	Require      *modfile.Require
	Replace      *modfile.Replace
	UseLocalCore bool
}

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run {httpjson} [flags] -- [go build flags]",
	Short: "Run current microservice as a server",
	Long: `The 'shana run' command is to run current microservice as a local server.
It's designed to be a development tool, not for production.

Flags after '--' will be passed to 'go build' command to build the service.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		defer errors.Handle(&err)

		projectRoot, pkgName, require, replace := findGoModule()
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
		var runningCommand atomic.Pointer[exec.Cmd]
		var interrupted atomic.Bool
		intChan := make(chan os.Signal, 2)
		exitChan := make(chan bool, 1)
		signal.Notify(intChan, os.Interrupt)
		run := func(c *exec.Cmd) error {
			runningCommand.Store(c)
			err := c.Run()
			runningCommand.Store(nil)
			return err
		}
		go func() {
			for {
				select {
				case <-intChan:
					interrupted.Store(true)

					if ptr := runningCommand.Load(); ptr != nil {
						ptr.Process.Signal(syscall.SIGTERM)
					}

					fmt.Fprintln(os.Stderr, "Caught SIGINT")

				case <-exitChan:
					signal.Stop(intChan)
				}
			}
		}()

		// Copy config file if exists.
		configFile := path.Join(projectRoot, shanaYAML)

		if stats, e := os.Stat(configFile); e == nil {
			if stats.IsDir() {
				errors.Throwf("Invalid config file: %v", configFile)
				return
			}

			errors.Check(os.Link(configFile, path.Join(cacheDir, shanaYAML)))
		}

		// Convert relative path to absolute path in replace.
		useLocalCore := false

		if replace != nil && replace.New.Version == "" {
			replace.New.Path = path.Clean(path.Join(projectRoot, replace.New.Path))
			useLocalCore = true
		}

		cmdContext := &cmdRunContext{
			PkgName:      pkgName,
			ProjectRoot:  projectRoot,
			ShanaCorePkg: shanaCorePackage,
			ServicePkgs:  pkgs,
			Require:      require,
			Replace:      replace,
			UseLocalCore: useLocalCore,
		}

		for _, tmpl := range tmpls {
			createFile(path.Join(cacheDir, tmpl.Name()), tmpl, cmdContext)
		}

		if interrupted.Load() {
			return
		}

		// Tidy the go.mod file.
		command := exec.Command("go", "mod", "tidy")
		command.Dir = cacheDir
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		errors.If(run(command)).Throw(errors.New("Fail to tidy the go.mod file."))

		if interrupted.Load() {
			return
		}

		// Build the service.
		goBuildArgs := []string{"build", "-o", shanaBuildServiceBinaryName}
		goBuildArgs = append(goBuildArgs, parseGoBuildFlags(args)...)
		command = exec.Command("go", goBuildArgs...)
		command.Dir = cacheDir
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		errors.If(run(command)).Throw(errors.New("Fail to build the service."))

		if interrupted.Load() {
			return
		}

		// Run the service.
		command = exec.Command("./" + shanaBuildServiceBinaryName)
		command.Dir = cacheDir
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		errors.If(run(command)).Throw(errors.New("Fail to run the service."))

		if interrupted.Load() {
			return
		}

		exitChan <- true
		return
	},
}

func findGoModule() (projectRoot, pkgName string, require *modfile.Require, replace *modfile.Replace) {
	command := exec.Command("go", "env", "GOMOD")
	output := &bytes.Buffer{}
	command.Stdout = output
	errors.If(command.Run()).Throw(errors.New("Fail to find go.mod in current project."))

	goMod := strings.TrimSpace(output.String())

	if goMod == os.DevNull {
		errors.Throwf("fail to find go.mod in current project.")
		return
	}

	data := errors.Check1(os.ReadFile(goMod))
	file := errors.Check1(modfile.Parse(goMod, data, nil))

	projectRoot = path.Dir(goMod)
	pkgName = file.Module.Mod.Path

	// Find the require and replace of github.com/go-shana/core.
	for _, req := range file.Require {
		if req.Mod.Path == shanaCorePackage {
			require = req
			break
		}
	}

	for _, rep := range file.Replace {
		if rep.Old.Path == shanaCorePackage {
			replace = rep
			break
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

		goModTemplate = template.Must(template.New("go.mod").Parse(`module github.com/go-shana/shana-workspace/debug-server

go 1.18

{{if .Require}}require {{.Require.Mod.Path}} {{.Require.Mod.Version}}{{end}}
{{if .Replace}}replace {{.Replace.Old.Path}} {{- .Replace.Old.Version}} => {{.Replace.New.Path}} {{- .Replace.New.Version}}{{end}}

replace {{.PkgName}} => {{.ProjectRoot}}
`))

		goWorkTemplate = template.Must(template.New("go.work").Parse(`go 1.18

use (
	.
	{{.ProjectRoot}}
)

{{if .UseLocalCore}}use {{.Replace.New.Path}}{{end}}
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
		errors.Throwf("unsupported server type '%v'", serverType)
	}

	return tmpls
}
