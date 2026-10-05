# cli Source

The cli source reads config from parsed flags via a cli.Context.

## Format

We expect the use of the `urfave/cli` package. Upper case flags will be lower cased. Dashes will be used as delimiters for nesting.

### Example

```go
micro.Flags(
    &cli.StringFlag{
        Name: "database-address",
        Value: "127.0.0.1",
        Usage: "the db address",
    },
    &cli.IntFlag{
        Name: "database-port",
        Value: 3306,
        Usage: "the db port",
    },
)
```

Becomes

```json
{
  "database": {
    "address": "127.0.0.1",
    "port": 3306
  }
}
```

## New and Load Source

Because a cli.Context is needed to retrieve the flags and their values, it is recommended to build your source from within a cli.Action.

The config source package is also named `cli`, so import it under another name:

```go
import (
    "log"

    "github.com/flylib/go-micro"
    "github.com/flylib/go-micro/config"
    "github.com/flylib/go-micro/config/source"
    clisource "github.com/flylib/go-micro/config/source/cli"
    "github.com/urfave/cli/v2"
)

func main() {
    // New Service
    service := micro.NewService(
        micro.Name("example"),
        micro.Flags(
            &cli.StringFlag{
                Name: "database-address",
                Value: "127.0.0.1",
                Usage: "the db address",
            },
        ),
    )

    var clisrc source.Source

    service.Init(
        micro.Action(func(c *cli.Context) error {
            clisrc = clisource.NewSource(
                clisource.Context(c),
            )
            // Alternatively, just setup your config right here
            return nil
        }),
    )

    // ... Load and use that source ...
    conf, err := config.NewConfig()
    if err != nil {
        log.Fatal(err)
    }
    conf.Load(clisrc)
}
```
