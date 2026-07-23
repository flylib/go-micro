# Errors

The framework's wire-level error type — errors that survive the trip across services.

## Concept

- `errors.Error{Id, Code, Detail, Status}` marshals to JSON; helpers `BadRequest/NotFound/Timeout/InternalServerError/...` set conventional HTTP-style codes.
- Servers encode handler errors into the response; clients `Parse` them back — so `errors.FromError(err)` on the caller side recovers the original code/detail across the network.
- Retry policies and gateways key off `Code` (e.g. 408/500 retryable, 4xx not).

```go
return errors.NotFound("orders", "order %s not found", id)
```
