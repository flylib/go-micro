# Logger

Structured, leveled logging used by the framework and available to handlers.

## Concept

- Interface: `Log/Logf(level, ...)`, `Fields(map) Logger` (returns a child logger with bound fields), levels Trace→Fatal with `Level.Enabled` filtering; Fatal exits.
- The framework logs through `logger.DefaultLogger` — swap it (or pass `service.Logger(...)`) and every component follows.
- Options: `WithLevel`, `WithOutput`, `WithFields`, `WithCallerSkipCount`.

## Implementations

built-in (default, slog-style text) · [slog](slog) (stdlib, zero deps) · [zap](zap) · [zerolog](zerolog) — the three adapters are standalone modules; each accepts an injected pre-configured instance (`WithLogger`).

```go
logger.DefaultLogger = zap.NewLogger(logger.WithLevel(logger.DebugLevel))
```
