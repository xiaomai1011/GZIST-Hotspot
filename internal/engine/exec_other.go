//go:build !windows

package engine

import "context"

// exec is only meaningful on Windows; the tethering APIs it wraps do not
// exist elsewhere.
func (e *Engine) exec(ctx context.Context, envReq string) (string, error) {
	return "", ErrNoAnswer
}
