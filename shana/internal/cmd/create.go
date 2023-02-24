package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"text/template"

	"github.com/go-shana/core/errors"
	"github.com/spf13/cobra"
)

type cmdCreateContext struct {
	PkgName      string
	Project      string
	ShanaCorePkg string
}

func init() {
	rootCmd.AddCommand(createCmd)
}

// createCmd represents the new command
var createCmd = &cobra.Command{
	Use:   "create module-name [directory] ",
	Short: "Create a new microservice project",
	Long: `The 'shana create' command is to create a new microservice project for start.
It will create a new directory with the package name of the project,
and generate files for a minimum microservice project.

For example:

	shana create repo.example.com/my-project

It will create a new directory named 'my-project' in the current directory.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		defer errors.Handle(&err)

		pkgName := args[0]

		if !pkgRegexp.MatchString(pkgName) {
			errors.Throwf("invalid module name: %v", pkgName)
			return
		}

		projectName := path.Base(pkgName)

		if len(args) > 1 {
			projectName = args[1]
		}

		errors.Check(os.MkdirAll(projectName, 0755))

		project := normalizeProjectName(projectName)
		cmdContext := &cmdCreateContext{
			PkgName:      pkgName,
			Project:      project,
			ShanaCorePkg: shanaCorePackage,
		}

		templates := listCreateTemplates()

		for _, tmpl := range templates {
			createFile(path.Join(projectName, tmpl.Name()), tmpl, cmdContext)
		}

		fmt.Fprintln(os.Stderr, "Project is created. Running 'go mod tidy' to install dependencies.")
		fmt.Fprintln(os.Stderr)

		command := exec.Command("go", "mod", "tidy")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.Dir = projectName
		errors.Check(command.Run())

		return
	},
}

// Make projectName be a valid package name.
func normalizeProjectName(projectName string) string {
	// If projectName starts with "go-", remove it.
	projectName = strings.TrimPrefix(projectName, "go-")

	// Remove all characters that are not letters, digits or underscore.
	projectName = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}

		return -1
	}, projectName)

	return projectName
}

var pkgRegexp = regexp.MustCompile(`^[a-zA-Z0-9_\-]+(\.[a-zA-Z0-9_\-]+)*(\/[a-zA-Z0-9_\-\.]+)*$`)

func listCreateTemplates() []*template.Template {

	var (
		goModTemplate = template.Must(template.New("go.mod").Parse(`module {{.PkgName}}

go 1.18
`))

		welcomeTemplate = template.Must(template.New("welcome.go").Parse(`package {{.Project}}

import (
	"context"

	"{{.ShanaCorePkg}}/rpc"
	"{{.ShanaCorePkg}}/validator/numeric"
)

func init() {
	// Export this method so it can be called by RPC clients.
	rpc.Export(Welcome)
}

type WelcomeRequest struct {
	Name string ` + "`json:\"name\"`" + `
}

type WelcomeResponse struct {
	Message string ` + "`json:\"message\"`" + `
}

// Validate validates the request.
// We can use validator and its subpackages to validate the request.
func (req *WelcomeRequest) Validate(ctx context.Context) {
	// Make sure the length of the name is between [1, 10).
	numeric.InRange(len(req.Name), 1, 10)
}

func Welcome(ctx context.Context, req *WelcomeRequest) (resp *WelcomeResponse, err error) {
	resp = &WelcomeResponse{
		Message: serviceConfig.Welcome + ", " + req.Name,
	}
	return
}
`))

		configTemplate = template.Must(template.New("config.go").Parse(`package {{.Project}}

import (
	"context"

	"{{.ShanaCorePkg}}/config"
)

var (
	// The config for this service.
	serviceConfig = config.New[Config]("service")
)

// Config is the config for this service.
// The struct field names will be uncaptilized and used as the config key.
type Config struct {
	Welcome string ` + "`shana:\"welcome\"`" + `
}

// Init initializes the config's default.
// It's optional.
func (c *Config) Init(ctx context.Context) {
	if c.Welcome == "" {
		c.Welcome = "Hello"
	}
}
`))

		shanaYAMLTemplate = template.Must(template.New("shana.yaml").Parse(`service:
  welcome: Hello

shana:
  # Set to true to turn on debug mode for development.
  debug: false
`))
	)

	return []*template.Template{
		goModTemplate,
		welcomeTemplate,
		configTemplate,
		shanaYAMLTemplate,
	}
}
