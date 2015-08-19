// Copyright 2014 Canonical Ltd.

package v1_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/juju/testing"
	"github.com/juju/testing/httptesting"
	gc "gopkg.in/check.v1"
	"gopkg.in/macaroon-bakery.v1/bakery"
	"gopkg.in/macaroon-bakery.v1/httpbakery"
	"gopkg.in/macaroon.v1"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"launchpad.net/lpad"

	"github.com/CanonicalLtd/blues-identity/idp"
	"github.com/CanonicalLtd/blues-identity/internal/identity"
	"github.com/CanonicalLtd/blues-identity/internal/mongodoc"
	"github.com/CanonicalLtd/blues-identity/internal/store"
	"github.com/CanonicalLtd/blues-identity/internal/v1"
	"github.com/CanonicalLtd/blues-identity/params"
)

const (
	version       = "v1"
	adminUsername = "admin"
	adminPassword = "password"
	location      = "https://0.1.2.3/identity"
)

type apiSuite struct {
	testing.IsolatedMgoSuite
	srv     *identity.Server
	pool    *store.Pool
	keyPair *bakery.KeyPair
	idps    []idp.IdentityProvider
}

var _ = gc.Suite(&apiSuite{})

func (s *apiSuite) SetUpSuite(c *gc.C) {
	s.IsolatedMgoSuite.SetUpSuite(c)
}

func (s *apiSuite) TearDownSuite(c *gc.C) {
	s.IsolatedMgoSuite.TearDownSuite(c)
}

func (s *apiSuite) SetUpTest(c *gc.C) {
	s.IsolatedMgoSuite.SetUpTest(c)

	key, err := bakery.GenerateKey()
	c.Assert(err, gc.IsNil)
	s.srv, s.pool = newServer(c, s.Session, key, s.idps)
	s.keyPair = key
}

func (s *apiSuite) TearDownTest(c *gc.C) {
	s.srv.Close()
	s.pool.Close()
	s.IsolatedMgoSuite.TearDownTest(c)
}

func fakeRedirectURL(_, _, _ string) (string, error) {
	return "http://0.1.2.3/nowhere", nil
}

func newServer(c *gc.C, session *mgo.Session, key *bakery.KeyPair, idps []idp.IdentityProvider) (*identity.Server, *store.Pool) {
	db := session.DB("testing")
	sp := identity.ServerParams{
		AuthUsername:      adminUsername,
		AuthPassword:      adminPassword,
		Key:               key,
		Location:          location,
		MaxMgoSessions:    50,
		Launchpad:         lpad.Production,
		IdentityProviders: idps,
	}
	pool, err := store.NewPool(db, store.StoreParams{
		AuthUsername:   sp.AuthUsername,
		AuthPassword:   sp.AuthPassword,
		Key:            sp.Key,
		Location:       sp.Location,
		MaxMgoSessions: sp.MaxMgoSessions,
		Launchpad:      sp.Launchpad,
	})
	c.Assert(err, gc.IsNil)
	srv, err := identity.New(
		db,
		sp,
		map[string]identity.NewAPIHandlerFunc{
			version: v1.NewAPIHandler,
		},
	)
	c.Assert(err, gc.IsNil)
	return srv, pool
}

func (s *apiSuite) assertMacaroon(c *gc.C, ms macaroon.Slice, check bakery.FirstPartyChecker) {
	store := s.pool.GetNoLimit()
	defer s.pool.Put(store)
	err := store.Service.Check(ms, check)
	c.Assert(err, gc.IsNil)
}

func (s *apiSuite) createUser(c *gc.C, user *params.User) (uuid string) {
	store := s.pool.GetNoLimit()
	defer s.pool.Put(store)
	httptesting.AssertJSONCall(c, httptesting.JSONCallParams{
		Handler: s.srv,
		URL:     apiURL("u/" + string(user.Username)),
		Method:  "PUT",
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:         marshal(c, user),
		Username:     adminUsername,
		Password:     adminPassword,
		ExpectStatus: http.StatusOK,
	})

	// Retrieve and return the newly created user's UUID.
	var id mongodoc.Identity
	err := store.DB.Identities().Find(
		bson.D{{"username", user.Username}},
	).Select(bson.D{{"baseurl", 1}}).One(&id)
	c.Assert(err, gc.IsNil)
	return id.UUID
}

func (s *apiSuite) createIdentity(c *gc.C, doc *mongodoc.Identity) (uuid string) {
	store := s.pool.GetNoLimit()
	defer s.pool.Put(store)
	err := store.UpsertIdentity(doc)
	c.Assert(err, gc.IsNil)
	return doc.UUID
}

func apiURL(path string) string {
	return "/" + version + "/" + path
}

// transport implements an http.RoundTripper that will intercept anly calls
// destined to a location starting with prefix and serves them using srv. For
// all other requests rt will be used.
type transport struct {
	prefix string
	srv    http.Handler
	rt     http.RoundTripper
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	dest := req.URL.String()
	if !strings.HasPrefix(dest, t.prefix) {
		return t.rt.RoundTrip(req)
	}
	var buf bytes.Buffer
	req.Write(&buf)
	sreq, _ := http.ReadRequest(bufio.NewReader(&buf))
	u, _ := url.Parse(t.prefix)
	sreq.URL.Path = strings.TrimPrefix(sreq.URL.Path, u.Path)
	sreq.RequestURI = strings.TrimPrefix(sreq.RequestURI, u.Path)
	sreq.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	t.srv.ServeHTTP(rr, sreq)
	return &http.Response{
		Status:        fmt.Sprintf("%d Status", rr.Code),
		StatusCode:    rr.Code,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rr.HeaderMap,
		Body:          ioutil.NopCloser(rr.Body),
		ContentLength: int64(rr.Body.Len()),
		Request:       req,
	}, nil
}

var DischargeRequiredBody httptesting.BodyAsserter = func(c *gc.C, body json.RawMessage) {
	var e httpbakery.Error
	err := json.Unmarshal(body, &e)
	c.Assert(err, gc.IsNil)
	c.Assert(e.Code, gc.Equals, httpbakery.ErrDischargeRequired)
}

// marshal converts a value into a bytes.Reader containing the json
// encoding of that value.
func marshal(c *gc.C, v interface{}) *bytes.Reader {
	b, err := json.Marshal(v)
	c.Assert(err, gc.Equals, nil)
	return bytes.NewReader(b)
}
