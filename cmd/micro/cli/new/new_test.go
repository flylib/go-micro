package new

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tmpl "github.com/flylib/go-micro/cmd/micro/cli/new/template"
)

// Every template renders to Go that parses, on the gRPC preset.
func TestTemplatesRenderGRPCServices(t *testing.T) {
	for _, name := range []string{"", "crud", "pubsub", "api"} {
		mainTmpl, handlerTmpl, _ := selectTemplates(name)
		dir := t.TempDir()
		c := config{Alias: "hello-world", Dir: "hello-world"}

		for file, text := range map[string]string{"main.go": mainTmpl, "handler.go": handlerTmpl} {
			path := filepath.Join(dir, file)
			if err := write(c, path, text); err != nil {
				t.Fatalf("template %q: %s: %v", name, file, err)
			}
			if _, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors); err != nil {
				t.Fatalf("template %q: %s does not parse: %v", name, file, err)
			}
		}

		src, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`grpcservice "github.com/flylib/go-micro/service/grpc"`,
			`grpcservice.New("hello-world")`,
		} {
			if !strings.Contains(string(src), want) {
				t.Errorf("template %q: main.go lacks %s", name, want)
			}
		}
		if strings.Contains(string(src), "micro.New(") {
			t.Errorf("template %q: main.go still builds a service on the default RPC server", name)
		}
	}
}

// The generated module requires the preset module.
func TestModuleRequiresThePreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.mod")
	if err := write(config{Alias: "hello", Dir: "hello"}, path, tmpl.Module); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github.com/flylib/go-micro latest", "github.com/flylib/go-micro/service/grpc latest"} {
		if !strings.Contains(string(mod), want) {
			t.Errorf("go.mod lacks %q:\n%s", want, mod)
		}
	}
}
