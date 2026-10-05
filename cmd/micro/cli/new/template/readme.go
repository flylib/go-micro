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
