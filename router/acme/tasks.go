package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	acme "github.com/eggsampler/acme/v3"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/attempt"
	router "github.com/flynn/flynn/router/types"
	"github.com/flynn/que-go"
	"github.com/google/martian/log"
	"github.com/inconshreveable/log15"
)

const (
	OrderCertificateTaskName = "acme.order_certificate"
	IssueCertificateTaskName = "acme.issue_certificate"

	responderPort = 8080
)

func NewOrderCertificateJob(cert *ct.ManagedCertificate) (*que.Job, error) {
	data, err := json.Marshal(cert)
	if err != nil {
		return nil, err
	}
	return &que.Job{
		Type: OrderCertificateTaskName,
		Args: data,
	}, nil
}

func NewIssueCertificateJob(cert *ct.ManagedCertificate) (*que.Job, error) {
	data, err := json.Marshal(cert)
	if err != nil {
		return nil, err
	}
	return &que.Job{
		Type: IssueCertificateTaskName,
		Args: data,
	}, nil
}

func RegisterJobHandlers(handlers que.WorkMap, client ControllerClient, log log15.Logger) {
	handlers[IssueCertificateTaskName] = JobHandler(client, log, IssueCertificate)
	handlers[OrderCertificateTaskName] = JobHandler(client, log, OrderCertificate)
}

func JobHandler(controller ControllerClient, log log15.Logger, runFunc runFunc) que.WorkFunc {
	return func(job *que.Job) error {
		ctx, err := NewTaskContext(job, controller, log)
		if err != nil {
			return err
		}
		return runFunc(ctx)
	}
}

type runFunc func(ctx *TaskContext) error

type TaskContext struct {
	cert       *ct.ManagedCertificate
	client     *acme.Client
	account    acme.Account
	controller ControllerClient
}

func NewTaskContext(job *que.Job, controller ControllerClient, log log15.Logger) (*TaskContext, error) {
	log = log.New("job.id", job.ID)

	log.Info("decoding managed certificate")
	var cert ct.ManagedCertificate
	if err := json.Unmarshal(job.Args, &cert); err != nil {
		log.Error("error decoding managed certificate", "err", err)
		return nil, err
	}

	log.Info(
		"initializing task",
		"cert.id", cert.ID().String(),
		"cert.domain", cert.Domain(),
		"cert.account.id", cert.Account.ID,
		"cert.account.directory_url", cert.Account.DirectoryURL,
	)

	log.Info("initializing ACME client")
	client, err := newClient(&cert.Account)
	if err != nil {
		log.Error("error initializing ACME client", "err", err)
		return nil, fmt.Errorf("error initializing ACME client: %s", err)
	}

	log.Info("loading ACME account", "id", cert.Account.ID)
	keyDER, err := controller.GetACMEAccountKey(cert.Account.ID)
	if err != nil {
		log.Error("error loading ACME account", "id", cert.Account.ID, "err", err)
		return nil, fmt.Errorf("error loading ACME account %q: %s", cert.Account.ID, err)
	}
	privKey, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		log.Error("error loading ACME account", "id", cert.Account.ID, "err", err)
		return nil, fmt.Errorf("error loading ACME account %q: %s", cert.Account.ID, err)
	}
	account, err := client.NewAccount(privKey, true, cert.Account.TermsOfServiceAgreed, cert.Account.Contacts...)
	if err != nil {
		log.Error("error loading ACME account", "id", cert.Account.ID, "err", err)
		return nil, fmt.Errorf("error loading ACME account %q: %s", cert.Account.ID, err)
	}

	return &TaskContext{
		cert:       &cert,
		client:     client,
		account:    account,
		controller: controller,
		log:        log.New("domain", cert.Domain()),
	}, nil

}

func OrderCertificate(ctx *TaskContext) error {
	return nil
}

func IssueCertificate(ctx *TaskContext) error {
	ctx.log.Info("initializing responder")

	responder, err := NewResponder(controller, discoverd.DefaultClient, fmt.Sprintf(":%d", defaultResponderPort), log)
	if err != nil {
		log.Error("error initializing responder", "err", err)
		return fmt.Errorf("error initializing responder: %s", err)
	}

	t.log.Info("running issue certificate task")

	// make sure we have an order
	order, err := t.getOrCreateOrder()
	if err != nil {
		t.setFailed("error creating order: %s", err)
		return err
	}

	// make sure the order has a http-01 challenge
	challenge := t.getHTTP01Challenge(order)
	if challenge == nil {
		t.setFailed("order does not have a http-01 challenge: %s", order.URL)
		return errors.New("missing http-01 challenge")
	}

	// satisfy the http-01 challenge
	if err := t.satisfyHTTP01Challenge(challenge); err != nil {
		t.setFailed("error satisfying http-01 challenge: %s", err)
		return err
	}

	// finalize the order
	order, keyDER, err := t.finalizeOrder(order)
	if err != nil {
		t.setFailed("error finalizing order: %s", err)
		return err
	}

	// set the current certificate to the newly issued certificate
	if err := t.setCertificate(keyDER, order); err != nil {
		t.setFailed("error setting current certificate: %s", err)
		return err
	}

	return nil
}

func (t *issueCertificateTask) getOrCreateOrder() (*acme.Order, error) {
	// if the managed certificate already has an OrderURL, fetch it and
	// check if it's valid
	if url := t.cert.OrderURL; url != "" {
		t.log.Info("checking existing order", "order.url", url)
		order, err := t.client.FetchOrder(t.account, url)
		if err != nil {
			t.log.Error("error checking existing order", "order.url", url, "err", err)
		} else if order.Status == "invalid" {
			t.log.Error("existing order is invalid", "order.url", url)
		} else {
			return &order, nil
		}
	}

	// the managed certificate either has no OrderURL, or the order the
	// OrderURL points to is not valid, so create a new order
	t.log.Info("creating new order")
	ids := []acme.Identifier{{Type: "dns", Value: t.cert.Domain()}}
	order, err := t.client.NewOrder(t.account, ids)
	if err != nil {
		t.log.Error("error creating new order", "err", err)
		return nil, err
	}

	t.log.Info("updating managed certificate with new order", "order.url", order.URL)
	t.cert.OrderURL = order.URL
	if err := t.controller.UpdateManagedCertificate(t.cert); err != nil {
		t.log.Error("error updating managed certificate with new order", "order.url", order.URL, "err", err)
		return nil, err
	}

	return &order, nil
}

func (t *issueCertificateTask) getHTTP01Challenge(order *acme.Order) *acme.Challenge {
	t.log.Info("getting http-01 challenge")
	for _, authURL := range order.Authorizations {
		auth, err := t.client.FetchAuthorization(t.account, authURL)
		if err != nil {
			continue
		}
		if challenge, ok := auth.ChallengeMap[acme.ChallengeTypeHTTP01]; ok {
			t.log.Info("using http-01 challenge", "challenge.token", challenge.Token, "challenge.url", challenge.URL)
			return &challenge
		}
	}
	t.log.Error("missing http-01 challenge")
	return nil
}

func (t *issueCertificateTask) satisfyHTTP01Challenge(challenge *acme.Challenge) error {
	log := t.log.New("challenge.token", challenge.Token)

	log.Info("configuring http-01 challenge response")
	return t.responder.RespondHTTP01(t.cert, challenge, func() error {
		// make multiple attempts to complete the challenge
		attempts := attempt.Strategy{
			Total: 60 * time.Second,
			Delay: 1 * time.Second,
		}
		return attempts.Run(func() error {
			// TODO: check the challenge is still valid

			// complete the challenge
			log.Info("completing http-01 challenge")
			if _, err := t.client.UpdateChallenge(t.account, *challenge); err != nil {
				log.Error("error completing http-01 challenge", "err", err)
				return err
			}

			return nil
		})
	})
}

func (t *issueCertificateTask) finalizeOrder(order *acme.Order) (*acme.Order, []byte, error) {
	t.log.Info("generating a CSR")
	// TODO: check cert.Config.KeyAlgo
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.log.Error("error generating private key", "err", err)
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.log.Error("error encoding private key", "err", err)
		return nil, nil, err
	}
	csrTmpl := &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
		PublicKeyAlgorithm: x509.ECDSA,
		PublicKey:          key.Public(),
		DNSNames:           []string{t.cert.Domain()},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, key)
	if err != nil {
		t.log.Error("error generating CSR", "err", err)
		return nil, nil, err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.log.Error("error generating CSR", "err", err)
		return nil, nil, err
	}

	t.log.Info("finalizing order")
	finalizedOrder, err := t.client.FinalizeOrder(t.account, *order, csr)
	if err != nil {
		t.log.Error("error finalizing order", "err", err)
		return nil, nil, err
	}

	return &finalizedOrder, keyDER, nil
}

func (t *issueCertificateTask) setCertificate(keyDER []byte, order *acme.Order) error {
	t.log.Info("fetching issued certificate")
	issuedCerts, err := t.client.FetchCertificates(t.account, order.Certificate)
	if err != nil {
		t.log.Error("error fetching issued certificate", "err", err)
		return err
	}
	t.cert.Status = ct.ManagedCertificateStatusIssued
	t.cert.Certificate = &router.Certificate{
		Chain: make([][]byte, len(issuedCerts)),
		Key:   keyDER,
	}
	for i, issuedCert := range issuedCerts {
		t.cert.Certificate.Chain[i] = issuedCert.Raw
	}

	t.log.Info("setting current certificate", "certificate.id", t.cert.Certificate.ID().String(), "key.id", t.cert.Certificate.KeyID().String())
	if err := t.controller.UpdateManagedCertificate(t.cert); err != nil {
		t.log.Error("error setting current certificate", "err", err)
		return err
	}

	return nil
}

func (t *issueCertificateTask) setFailed(format string, v ...interface{}) {
	detail := fmt.Sprintf(format, v...)
	t.log.Info("setting certificate status to failed", "detail", detail)
	t.cert.Status = ct.ManagedCertificateStatusFailed
	t.cert.AddError("", detail)
	t.controller.UpdateManagedCertificate(t.cert)
}
