package verify

import (
	"context"

	"github.com/google/go-containerregistry/pkg/name"
)

type Verifier interface {
	Verify(context.Context, name.Reference, string) (bool, error)
}
