package template

var (
	Readme = `# {{title .Alias}} Service

Generated with

` + "```" + `
micro new {{.Alias}}
` + "```" + `

## Getting Started

Generate the proto code:

` + "```bash" + `
make proto
` + "```" + `

Run the service:

` + "```bash" + `
go run .
` + "```" + `

## Configuration

The service runs on gRPC and reads its settings from the environment:

| Variable | Example | Meaning |
|---|---|---|
| ` + "`MICRO_SERVER_ADDRESS`" + ` | ` + "`:9001`" + ` | Listen address. Set a fixed port for deployments. |
| ` + "`MICRO_REGISTRY`" + `, ` + "`MICRO_REGISTRY_ADDRESS`" + ` | ` + "`nacos`" + `, ` + "`nacos:8848`" + ` | Service registry (default: mDNS, for local development) |
| ` + "`MICRO_BROKER`" + `, ` + "`MICRO_BROKER_ADDRESS`" + ` | ` + "`nats`" + `, ` + "`nats:4222`" + ` | Message broker |

A registry or broker is selected by name once its package is linked in, with
a blank import in main.go such as ` + "`_ \"github.com/flylib/go-micro/registry/nacos\"`" + `.

## Call the service

With the service running:

` + "```bash" + `
micro call {{lower .Alias}} {{title .Alias}}.Call '{"name": "Alice"}'
` + "```" + `

### Documenting handlers

Doc comments on handler methods are registered as endpoint descriptions. They show up in ` + "`micro describe`" + ` and on the service page of the ` + "`micro run`" + ` dashboard:

` + "```go" + `
// CreateUser registers a new user account with the given email and name.
// Returns the created user with their assigned ID.
func (s *Users) CreateUser(ctx context.Context, req *CreateRequest, rsp *CreateResponse) error {
    // ...
}
` + "```" + `

## Development

` + "```bash" + `
make proto    # Regenerate proto code
make build    # Build binary
make test     # Run tests
make dev      # Run with hot reload (requires air)
` + "```" + `
`
)
