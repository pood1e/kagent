package executionauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kagent-dev/kagent/go/core/internal/jwttest"
)

func TestAssertionsAreSingleUseAndVerifiableFromPublishedKeys(t *testing.T) {
	signer, err := newAssertionSigner(assertionIssuer, jwttest.AssertionKey(t))
	require.NoError(t, err)
	keys := jwttest.PublishedKeys(t, signer)
	endpoint := "https://identity.example.test/realms/site/protocol/openid-connect/token"
	first, err := signer.sign("alice", endpoint)
	require.NoError(t, err)
	second, err := signer.sign("alice", endpoint)
	require.NoError(t, err)
	verified, err := jwttest.VerifyAssertion(keys, first, assertionIssuer, endpoint)
	require.NoError(t, err)
	require.Equal(t, "alice", verified.Subject)
	again, err := jwttest.VerifyAssertion(keys, second, assertionIssuer, endpoint)
	require.NoError(t, err)
	require.NotEqual(t, verified.ID, again.ID)
	_, err = jwttest.VerifyAssertion(keys, first, assertionIssuer, "https://other.example.test/token")
	require.Error(t, err)
	other, err := newAssertionSigner(assertionIssuer, jwttest.AssertionKey(t))
	require.NoError(t, err)
	_, err = jwttest.VerifyAssertion(jwttest.PublishedKeys(t, other), first, assertionIssuer, endpoint)
	require.Error(t, err, "a different controller key must not verify")
}

func TestAssertionKeyRequiresOneEd25519PKCS8Block(t *testing.T) {
	directory := t.TempDir()
	for name, material := range map[string]string{
		"symmetric": "0123456789abcdef0123456789abcdef",
		"two keys":  string(must(os.ReadFile(jwttest.AssertionKey(t)))) + string(must(os.ReadFile(jwttest.AssertionKey(t)))),
	} {
		path := filepath.Join(directory, name)
		require.NoError(t, os.WriteFile(path, []byte(material), 0o600))
		_, err := newAssertionSigner(assertionIssuer, path)
		require.Error(t, err, name)
	}
	_, err := newAssertionSigner(assertionIssuer, filepath.Join(directory, "missing"))
	require.Error(t, err)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
