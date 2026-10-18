package estimate

import (
	"context"

	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/links"
)

//go:generate moq -out moq_test.go . decider linkFetcher

type decider interface {
	SystemOne(ctx context.Context, req jev.Request) (jev.Response, error)
}

type linkFetcher interface {
	Fetch(ctx context.Context, link links.Link) (string, error)
}
