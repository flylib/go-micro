# File Source

The file source reads config from a file.

It uses the File extension to determine the Format e.g `config.json` has the json format.
It does not make use of encoders or interpet the file data. If a file extension is not present
the source Format will default to the Encoder in options.

## Example

A config file format in json

```json
{
  "hosts": {
    "database": {
      "address": "10.0.0.1",
      "port": 3306
    },
    "cache": {
      "address": "10.0.0.2",
      "port": 6379
    }
  }
}
```

## New Source

Specify file source with path to file. Path is optional and will default to `config.json`

```go
fileSource := file.NewSource(
	file.WithPath("/tmp/config.json"),
)
```

## File Format

Only JSON is built in: [config/encoder](../../encoder/) has just the `json` encoder, and the default reader only registers `json`. A file whose format has no registered encoder is decoded as JSON.

To load another format e.g yaml, implement `encoder.Encoder` with `String()` returning the file extension, and register it with the reader

```go
var e encoder.Encoder = yamlEncoder{} // your implementation; String() returns "yaml"

conf, err := config.NewConfig(
        config.WithReader(json.NewReader(reader.WithEncoder(e))),
)

fileSource := file.NewSource(
        file.WithPath("/tmp/config.yaml"),
)
```

Here `json` is `github.com/flylib/go-micro/config/reader/json`, the default reader, which merges any registered format into JSON.

If you want to specify a file without extension, also set the source encoder to the same format

```go
fileSource := file.NewSource(
        file.WithPath("/tmp/config"),
        source.WithEncoder(e),
)
```

## Load Source

Load the source into config

```go
// Create new config
conf, err := config.NewConfig()
if err != nil {
	log.Fatal(err)
}

// Load file source
conf.Load(fileSource)
```
