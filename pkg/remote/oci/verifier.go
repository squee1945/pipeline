package oci

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

type Verifier interface {
	Verify(context.Context, name.Reference, string, authn.Keychain) (bool, error)
}

func lookupVerifier(signer string) (Verifier, error) {
	if signer == "cosign" {
		return &cosignVerifier{}, nil
	}
	return nil, fmt.Errorf("unknown signer %q", signer)
}
