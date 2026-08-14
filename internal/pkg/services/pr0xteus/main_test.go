package pr0xteus

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))

	code := m.Run()
	slog.SetDefault(previousLogger)
	os.Exit(code)
}
