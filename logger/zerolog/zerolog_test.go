package zerolog

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flylib/go-micro/logger"
)

func TestZerologLogger(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(logger.WithOutput(&buf), logger.WithLevel(logger.InfoLevel))

	l.Logf(logger.InfoLevel, "hello %s", "world")
	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("missing message: %q", buf.String())
	}

	buf.Reset()
	l.Log(logger.DebugLevel, "should be filtered")
	if buf.Len() != 0 {
		t.Fatalf("debug should be filtered at info level: %q", buf.String())
	}

	buf.Reset()
	l.Fields(map[string]interface{}{"svc": "orders"}).Log(logger.WarnLevel, "warned")
	out := buf.String()
	if !strings.Contains(out, `"svc":"orders"`) || !strings.Contains(out, "warned") {
		t.Fatalf("fields lost: %q", out)
	}
	if l.String() != "zerolog" {
		t.Fatal("wrong name")
	}
}
