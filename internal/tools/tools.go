//go:build tools

// Package tools pins development tool versions in go.mod.
package tools

import (
	_ "github.com/matryer/moq"
	_ "mvdan.cc/gofumpt"
)
