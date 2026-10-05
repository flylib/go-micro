# Hello World Example

The simplest go-micro service demonstrating core concepts.

## What It Does

This example creates a basic RPC service that:
- Listens on port 8080
- Exposes a `Greeter.Hello` method
- Returns a greeting message
- Can be called over HTTP with `curl` or with `micro call`

## Run It

```bash
go run main.go
```

The service registers itself, prints a sample `curl` command and waits for incoming requests.

## Test It

### Using curl

```bash
curl -X POST http://localhost:8080 \
  -H 'Content-Type: application/json' \
  -H 'Micro-Endpoint: Greeter.Hello' \
  -d '{"name": "Alice"}'
```

Expected response:
```json
{"message": "Hello Alice"}
```

### Using the micro CLI

```bash
micro call greeter Greeter.Hello '{"name": "Bob"}'
```

## Code Walkthrough

1. **Define types** - Request and Response structures
2. **Implement handler** - The `Greeter` service with `Hello` method
3. **Create service** - Using `micro.New()` with options
4. **Register handler** - Link the handler to the service
5. **Run service** - Start listening for requests

## Key Concepts

- **RPC Pattern**: Method signature `func(ctx, req, rsp) error`
- **Service Discovery**: Automatic registration
- **Multiple Transports**: Works over HTTP, gRPC, etc.
- **Type Safety**: Strongly typed requests/responses

## Next Steps

- See [multi-service](../multi-service/) for several services in one project
- See [auth](../auth/) for auth wrappers and scopes
- Read the [README](../../README.md)
