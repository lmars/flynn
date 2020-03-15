package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"fmt"

	acme "github.com/eggsampler/acme/v3"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/stream"
	router "github.com/flynn/flynn/router/types"
)

// DefaultDirectoryURL is the default ACME directory URL
const DefaultDirectoryURL = acme.LetsEncryptStaging

// ControllerClient is an interface that provides streaming and updating of managed
// certificates, and the creation and deletion of routes
type ControllerClient interface {
	StreamManagedCertificates(certs chan *ct.ManagedCertificate) (stream.Stream, error)

	UpdateManagedCertificate(cert *ct.ManagedCertificate) error

	GetACMEAccountKey(accountID router.ID) ([]byte, error)

	CreateRoute(appID string, route *router.Route) error

	DeleteRoute(appID string, routeID string) error
}

// CheckAccountExists checks that the given ACME account exists
func CheckAccountExists(account *ct.ACMEAccount, keyPEM []byte) error {
	client, err := newClient(account)
	if err != nil {
		return err
	}
	keyDER, err := router.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return err
	}
	privKey, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return err
	}
	_, err = client.NewAccount(privKey, true, account.TermsOfServiceAgreed, account.Contacts...)
	return err
}

// CreateAccount creates the given ACME account
func CreateAccount(account *ct.ACMEAccount) ([]byte, error) {
	client, err := newClient(account)
	if err != nil {
		return nil, err
	}
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("error generating ACME account key: %s", err)
	}
	if _, err := client.NewAccount(privKey, false, account.TermsOfServiceAgreed, account.Contacts...); err != nil {
		return nil, fmt.Errorf("error creating ACME account: %s", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("error encoding private key: %s", err)
	}
	return keyDER, nil
}

func newClient(account *ct.ACMEAccount) (*acme.Client, error) {
	directoryURL := account.DirectoryURL
	if directoryURL == "" {
		directoryURL = DefaultDirectoryURL
	}
	client, err := acme.NewClient(directoryURL)
	if err != nil {
		return nil, err
	}
	return &client, nil
}
