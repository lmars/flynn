package bootstrap

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/flynn/flynn/router/acme"
)

type GenACMEAccountAction struct {
	ID string `json:"id"`

	ACMEConfig
}

func init() {
	Register("gen-acme-account", &GenACMEAccountAction{})
}

type ACMEConfig struct {
	DirectoryURL         string `json:"directory_url"`
	Key                  string `json:"key"`
	Contacts             string `json:"contacts"`
	TermsOfServiceAgreed string `json:"terms_of_service_agreed"`
}

func (a *GenACMEAccountAction) Run(s *State) error {
	data := a.ACMEConfig
	s.StepData[a.ID] = &data

	// interpolate the config
	data.DirectoryURL = interpolate(s, data.DirectoryURL)
	data.Key = interpolate(s, data.Key)
	data.Contacts = interpolate(s, data.Contacts)
	data.TermsOfServiceAgreed = interpolate(s, data.TermsOfServiceAgreed)

	// construct an ACME account
	account := &ct.ACMEAccount{
		DirectoryURL: data.DirectoryURL,
	}
	if len(data.Contacts) > 0 {
		account.Contacts = strings.Split(data.Contacts, ",")
	}
	if v, err := strconv.ParseBool(data.TermsOfServiceAgreed); err == nil {
		account.TermsOfServiceAgreed = v
	}

	// if the key is set, check the account exists
	if data.Key != "" {
		if err := acme.CheckAccountExists(account, []byte(data.Key)); err != nil {
			return fmt.Errorf("error checking existing ACME account: %s", err)
		}
		return nil
	}

	// create the account
	key, err := acme.CreateAccount(account)
	if err != nil {
		return err
	}
	client, err := s.ControllerClient()
	if err != nil {
		return err
	}
	if err := client.CreateACMEAccount(account, key); err != nil {
		return fmt.Errorf("error creating ACME account: %s", err)
	}

	return nil
}
