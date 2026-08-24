package github

import (
	"fmt"
	"strings"
)

const MaxPaginationPages = 128

type paginationGuard struct {
	operation string
	pages     int
	seen      map[string]struct{}
}

func newPaginationGuard(operation string) *paginationGuard {
	return &paginationGuard{operation: operation, seen: map[string]struct{}{}}
}

func (g *paginationGuard) startPage() error {
	if g.pages >= MaxPaginationPages {
		return fmt.Errorf("%s pagination exceeded fixed limit of %d pages", g.operation, MaxPaginationPages)
	}
	g.pages++
	return nil
}

func (g *paginationGuard) advance(current, next string) (string, error) {
	next = strings.TrimSpace(next)
	if next == "" {
		return "", fmt.Errorf("%s pagination returned a missing cursor after page %d", g.operation, g.pages)
	}
	if next == current {
		return "", fmt.Errorf("%s pagination returned a non-advancing cursor after page %d", g.operation, g.pages)
	}
	if _, duplicate := g.seen[next]; duplicate {
		return "", fmt.Errorf("%s pagination returned a repeated cursor after page %d", g.operation, g.pages)
	}
	g.seen[next] = struct{}{}
	return next, nil
}
