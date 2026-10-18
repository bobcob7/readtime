package estimate

import (
	"context"

	"github.com/bobcob7/readtime/internal/jev"
)

//go:generate moq -out moq_test.go . decider

type decider interface {
	SystemOne(ctx context.Context, req jev.Request) (jev.Response, error)
}
