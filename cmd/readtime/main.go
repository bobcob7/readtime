// Command readtime estimates how long it takes different audiences to read, understand and grok text piped to it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/bobcob7/readtime/internal/estimate"
	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/report"
)

const maxInputBytes = 16 << 20

var (
	errMissingAPIKey = errors.New("TYPESAFE_API_KEY is not set")
	errNoInput       = errors.New("no input: pipe text to stdin, e.g. `cat essay.md | readtime`")
	errInputTooLarge = errors.New("input exceeds 16 MiB")
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "readtime: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("readtime", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON instead of a table")
	model := fs.String("model", jev.DefaultModel, "Jev model to query")
	timeout := fs.Duration("timeout", 2*time.Minute, "overall time limit")
	verbose := fs.Bool("v", false, "log API activity to stderr")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: <text> | readtime [flags]\n\nEstimates read, understand and grok times per audience using TypeSafe Jev.\nRequires TYPESAFE_API_KEY.\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		return errMissingAPIKey
	}
	text, err := readStdin(os.Stdin)
	if err != nil {
		return err
	}
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := jev.NewClient(&http.Client{}, jev.DefaultBaseURL, apiKey, logger)
	estimator := estimate.NewEstimator(client, *model, logger)
	rep, err := estimator.Estimate(ctx, text)
	if err != nil {
		return fmt.Errorf("estimating: %w", err)
	}
	if *jsonOut {
		return report.JSON(os.Stdout, rep)
	}
	return report.Table(os.Stdout, rep)
}

func readStdin(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("inspecting stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errNoInput
	}
	data, err := io.ReadAll(io.LimitReader(f, maxInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	if len(data) > maxInputBytes {
		return "", errInputTooLarge
	}
	return string(data), nil
}
