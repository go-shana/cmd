package cmd

import (
	"os"
	"text/template"

	"github.com/go-shana/core/errors"
	"github.com/spf13/cobra"
)

const (
	shanaCorePackage = "github.com/go-shana/core"
	shanaYAML        = "shana.yaml"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "shana",
	Short: "A tool is to create, build and debug Shana microservice.",
	Long:  `The shana is a tool to create, build and debug Shana microservice.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func createFile(filename string, tmpl *template.Template, data any) {
	f := errors.Check1(os.Create(filename))
	defer f.Close()
	errors.Check(tmpl.Execute(f, data))
}

func isFileExists(filename string) bool {
	if stats, e := os.Stat(filename); e == nil {
		if stats.IsDir() {
			return false
		}

		return true
	}

	return false
}
