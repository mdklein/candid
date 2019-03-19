// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package discharger_test

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/frankban/quicktest/qtsuite"
	"gopkg.in/CanonicalLtd/candidclient.v1/params"
	"gopkg.in/macaroon-bakery.v2/bakery/identchecker"
	"gopkg.in/macaroon-bakery.v2/httpbakery"

	"github.com/CanonicalLtd/candid/idp"
	"github.com/CanonicalLtd/candid/idp/static"
	"github.com/CanonicalLtd/candid/internal/auth"
	"github.com/CanonicalLtd/candid/internal/candidtest"
	"github.com/CanonicalLtd/candid/internal/discharger"
	"github.com/CanonicalLtd/candid/internal/identity"
)

func TestLogin(t *testing.T) {
	qtsuite.Run(qt.New(t), &loginSuite{})
}

type loginSuite struct {
	store            *candidtest.Store
	srv              *candidtest.Server
	dischargeCreator *candidtest.DischargeCreator
	interactor       httpbakery.WebBrowserInteractor
}

func (s *loginSuite) Init(c *qt.C) {
	s.store = candidtest.NewStore()
	sp := s.store.ServerParams()
	sp.IdentityProviders = []idp.IdentityProvider{
		static.NewIdentityProvider(static.Params{
			Name: "test",
			Users: map[string]static.UserInfo{
				"test": {
					Password: "testpassword",
					Name:     "Test User",
					Email:    "test@example.com",
					Groups:   []string{"test1", "test2"},
				},
			},
		}),
		static.NewIdentityProvider(static.Params{
			Name:   "test2",
			Domain: "test2",
		}),
	}
	s.srv = candidtest.NewServer(c, sp, map[string]identity.NewAPIHandlerFunc{
		"discharger": discharger.NewAPIHandler,
	})
	s.dischargeCreator = candidtest.NewDischargeCreator(s.srv)
	s.interactor = httpbakery.WebBrowserInteractor{
		OpenWebBrowser: candidtest.PasswordLogin(c, "test", "testpassword"),
	}
}

func (s *loginSuite) TestLegacyInteractiveLogin(c *qt.C) {
	client := s.srv.Client(s.interactor)
	// Use "<is-authenticated-user" to force legacy interaction
	ms, err := s.dischargeCreator.Discharge(c, "<is-authenticated-user", client)
	c.Assert(err, qt.Equals, nil)
	s.dischargeCreator.AssertMacaroon(c, ms, identchecker.LoginOp, "test")
}

func (s *loginSuite) TestLegacyNonInteractiveLogin(c *qt.C) {
	client := s.srv.AdminClient()
	// Use "<is-authenticated-user" to force legacy interaction
	ms, err := s.dischargeCreator.Discharge(c, "<is-authenticated-user", client)
	c.Assert(err, qt.Equals, nil)
	s.dischargeCreator.AssertMacaroon(c, ms, identchecker.LoginOp, auth.AdminUsername)
}

func (s *loginSuite) TestLegacyLoginFailure(c *qt.C) {
	client := s.srv.Client(httpbakery.WebBrowserInteractor{
		OpenWebBrowser: candidtest.PasswordLogin(c, "test", "badpassword"),
	})
	// Use "<is-authenticated-user" to force legacy interaction
	_, err := s.dischargeCreator.Discharge(c, "<is-authenticated-user", client)
	c.Assert(err, qt.ErrorMatches, `cannot get discharge from ".*": failed to acquire macaroon after waiting: third party refused discharge: authentication failed for user "test"`)
}

func (s *loginSuite) TestInteractiveLogin(c *qt.C) {
	client := s.srv.Client(s.interactor)
	ms, err := s.dischargeCreator.Discharge(c, "is-authenticated-user", client)
	c.Assert(err, qt.Equals, nil)
	s.dischargeCreator.AssertMacaroon(c, ms, identchecker.LoginOp, "test")
}

func (s *loginSuite) TestNonInteractiveLogin(c *qt.C) {
	client := s.srv.AdminClient()
	ms, err := s.dischargeCreator.Discharge(c, "is-authenticated-user", client)
	c.Assert(err, qt.Equals, nil)
	s.dischargeCreator.AssertMacaroon(c, ms, identchecker.LoginOp, auth.AdminUsername)
}

func (s *loginSuite) TestLoginFailure(c *qt.C) {
	client := s.srv.Client(httpbakery.WebBrowserInteractor{
		OpenWebBrowser: candidtest.PasswordLogin(c, "test", "badpassword"),
	})
	_, err := s.dischargeCreator.Discharge(c, "is-authenticated-user", client)
	c.Assert(err, qt.ErrorMatches, `cannot get discharge from ".*": cannot acquire discharge token: authentication failed for user "test"`)
}

func (s *loginSuite) TestLoginMethodsIncludesAgent(c *qt.C) {
	req, err := http.NewRequest("GET", "/login-legacy", nil)
	c.Assert(err, qt.Equals, nil)
	req.Header.Set("Accept", "application/json")
	resp := s.srv.Do(c, req)
	defer resp.Body.Close()
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	buf, err := ioutil.ReadAll(resp.Body)
	c.Assert(err, qt.Equals, nil)
	var lm params.LoginMethods
	err = json.Unmarshal(buf, &lm)
	c.Assert(err, qt.Equals, nil)
	c.Assert(lm.Agent, qt.Equals, s.srv.URL+"/login/legacy-agent")
}
