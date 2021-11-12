package oci

import (
	"context"
	"crypto"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ociremote "github.com/google/go-containerregistry/pkg/remote"
	"github.com/sigstore/cosign/pkg/cosign"
	"github.com/sigstore/cosign/pkg/cosign/pkcs11key"
	cosignremote "github.com/sigstore/cosign/pkg/cosign/pkg/remote"
	cosignsignature "github.com/sigstore/cosign/pkg/cosign/pkg/signature"
	"github.com/sigstore/sigstore/pkg/signature"
)

type cosignVerifier struct{}

func (c *cosignVerifier) Verify(ctx context.Context, imgRef name.Reference, key string, keychain authn.Keychain) (bool, error) {
	remoteOpts := []ociremote.Option{ociremote.WithAuthFromKeychain(keychain), ociremote.WithContext(ctx)}
	clientOpts := []cosignremote.Option{cosignremote.WithRemoteOptions(remoteOpts...)}
	cosignOpts := &cosign.CheckOpts{
		Annotations:        map[string]interface{}{},
		RegistryClientOpts: clientOpts,
		// CertEmail:          c.CertEmail,
	}
	var pubKey signature.Verifier
	if key != "" {
		// TODO: Validate this earlier?
		var err error
		if strings.HasPrefix(strings.TrimSpace(key), "-----BEGIN PUBLIC KEY-----") {
			ecdsa, err := cosign.PemToECDSAKey([]byte(strings.TrimSpace(key)))
			if err != nil {
				return false, fmt.Errorf("converting pem to ecdsa: %v", err)
			}
			cosignOpts.SigVerifier, err = signature.LoadECDSAVerifier(ecdsa, crypto.SHA256)
		} else {
			cosignOpts.SigVerifier, err = cosignsignature.PublicKeyFromKeyRef(ctx, key)
		}
		if err != nil {
			return false, fmt.Errorf("loading key: %v", err)
		}
		pkcs11Key, ok := pubKey.(*pkcs11key.Key)
		if ok {
			defer pkcs11Key.Close()
		}
	}
	// if c.CheckClaims {
	// 	co.ClaimVerifier = cosign.SimpleClaimVerifier
	// }
	// if options.EnableExperimental() {
	// 	co.RekorURL = c.RekorURL
	// 	co.RootCerts = fulcio.GetRoots()
	// }
	_, verified, err := cosign.VerifyImageSignatures(ctx, imgRef, cosignOpts)
	if err != nil {
		return false, err
	}
	if !verified {
		return false, fmt.Errorf("signature on %s was not verified", imgRef)
	}

	// TODO: Do I need to look at the verified payloads and match the digest to the resolved image digest?

	return true, nil
}
