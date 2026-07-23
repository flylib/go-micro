# Auth

Authentication and authorization — accounts, tokens and per-endpoint access rules.

## Concept

- `Generate` creates an `Account{ID, Type, Scopes, Metadata}`; `Token` issues/refreshes credentials; `Inspect` validates a token back into an account; `Verify(account, resource)` enforces `Rules`.
- Wire-level integration lives in [wrapper/auth](../wrapper/auth): the client wrapper attaches the token header, the server wrapper inspects and verifies it per request.

## Implementations

noop (default — everything allowed) · JWT (`auth/jwt`, own module; RSA-signed tokens, vendored token codec in `auth/jwt/token`).
