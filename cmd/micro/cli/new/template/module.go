package template

var (
	Module = `module {{.Dir}}

go 1.22

require (
	github.com/flylib/go-micro latest
	github.com/golang/protobuf latest
	google.golang.org/protobuf latest
)
`
)
