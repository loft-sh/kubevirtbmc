package redfish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

// ServiceRoot advertising a resource that answers 501 is the one combination a
// Redfish client cannot recover from: nv-redfish maps an absent nav property to
// "unsupported" and degrades to Basic auth, but maps an error on an advertised
// one to a hard failure. So the advertisement and the handler have to stay in
// step, and this pins them together rather than testing either alone.
func TestServiceRoot_AdvertisesSessionService(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetServiceRoot())

	svc, ok := body["SessionService"].(map[string]any)
	require.True(t, ok, "ServiceRoot must advertise SessionService, got %T", body["SessionService"])
	assert.Equal(t, "/redfish/v1/SessionService", svc["@odata.id"])
}

func TestGetSessionService_ServedWithSessionsLink(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetSessionService())

	assert.Equal(t, "/redfish/v1/SessionService", body["@odata.id"])
	assert.Equal(t, "SessionService", body["Id"])
	assert.Equal(t, "Session Service", body["Name"])
	assert.Equal(t, true, body["ServiceEnabled"],
		"virtbmc really does create sessions, so it must not claim otherwise")

	// A SessionService without this link fails the client with "does not expose
	// a Sessions collection", which is outside the Basic-auth fallback path.
	sessions, ok := body["Sessions"].(map[string]any)
	require.True(t, ok, "SessionService must link to its Sessions collection, got %T", body["Sessions"])
	assert.Equal(t, "/redfish/v1/SessionService/Sessions", sessions["@odata.id"])
}

func TestGetSessionCollection_CountMatchesMembers(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetSessionCollection())

	assert.Equal(t, "/redfish/v1/SessionService/Sessions", body["@odata.id"])

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must be present and be an array, got %T", body["Members"])

	count, ok := body["Members@odata.count"].(float64)
	require.True(t, ok, "Members@odata.count must be present")
	assert.Equal(t, float64(len(members)), count)
}

// A session that was created must be findable in the collection, because that
// is how a client revokes the session it created last time.
func TestGetSessionCollection_ListsLiveSessions(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	id, _, err := h.Authenticate(&[]string{testUsername}[0], &[]string{testPassword}[0])
	require.NoError(t, err)
	t.Cleanup(func() { h.DeleteSession(id) })

	body := asMap(t, h.GetSessionCollection())
	members, _ := body["Members"].([]any)

	want := fmt.Sprintf("/redfish/v1/SessionService/Sessions/%s", id)
	found := false
	for _, m := range members {
		if m.(map[string]any)["@odata.id"] == want {
			found = true
		}
	}
	assert.True(t, found, "session %s must appear in the collection, got %v", id, members)
	assert.Equal(t, float64(len(members)), body["Members@odata.count"])
}

// DeleteSession used to hand a session ID to RemoveToken, which is keyed by
// token, so it deleted nothing and every session stayed valid forever.
func TestDeleteSession_ActuallyRevokes(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	id, token, err := h.Authenticate(&[]string{testUsername}[0], &[]string{testPassword}[0])
	require.NoError(t, err)

	_, exists := session.GetToken(token)
	require.True(t, exists, "precondition: the token must be live")

	assert.True(t, h.DeleteSession(id), "deleting a live session must report success")

	_, exists = session.GetToken(token)
	assert.False(t, exists, "the token must no longer authenticate")

	assert.False(t, h.DeleteSession(id), "deleting a gone session must report failure")
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	api := NewAPIService(testUsername, testPassword, nil)
	router := server.NewRouter(
		session.AuthMiddleware(testUsername, testPassword),
		server.NewDefaultAPIController(api),
	)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any, http.Header) {
	t.Helper()

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
	require.NoError(t, err)
	req.SetBasicAuth(testUsername, testPassword)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)

	return resp.StatusCode, decoded, resp.Header
}

// The status codes are the point of this fix, and only an HTTP-level test sees
// them: a handler returning a valid document is worthless if the route still
// answers 501. This walks the exact path nv-redfish walks - ServiceRoot, then
// the advertised SessionService, then the Sessions collection it links to -
// then creates, finds and revokes a session over the wire.
func TestSessionServiceWalk_NoNotImplementedOnTheClientPath(t *testing.T) {
	srv := newTestServer(t)

	// GET /redfish/v1 is a redirect to the trailing-slash form; the client
	// reads the latter.
	status, root, _ := do(t, srv, http.MethodGet, "/redfish/v1/", nil)
	require.Equal(t, http.StatusOK, status)
	svcRef, ok := root["SessionService"].(map[string]any)
	require.True(t, ok, "ServiceRoot must advertise SessionService")
	svcPath := svcRef["@odata.id"].(string)

	status, svc, _ := do(t, srv, http.MethodGet, svcPath, nil)
	require.Equal(t, http.StatusOK, status,
		"an advertised SessionService that errors gives the client no fallback")
	sessionsPath := svc["Sessions"].(map[string]any)["@odata.id"].(string)

	status, collection, _ := do(t, srv, http.MethodGet, sessionsPath, nil)
	require.Equal(t, http.StatusOK, status,
		"nv-redfish GETs the Sessions collection right after the service; a 501 here is equally fatal")
	before := len(collection["Members"].([]any))

	status, created, headers := do(t, srv, http.MethodPost, sessionsPath, map[string]string{
		"UserName": testUsername,
		"Password": testPassword,
	})
	require.Equal(t, http.StatusCreated, status)
	id, ok := created["Id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, headers.Get("X-Auth-Token"), "the client needs the token to use the session")

	sessionPath := fmt.Sprintf("%s/%s", sessionsPath, id)
	assert.Equal(t, sessionPath, headers.Get("Location"))
	assert.Equal(t, sessionPath, created["@odata.id"],
		"the document must agree with the Location header the client stored")

	status, collection, _ = do(t, srv, http.MethodGet, sessionsPath, nil)
	require.Equal(t, http.StatusOK, status)
	members := collection["Members"].([]any)
	assert.Len(t, members, before+1)
	assert.Equal(t, float64(len(members)), collection["Members@odata.count"])

	status, _, _ = do(t, srv, http.MethodGet, sessionPath, nil)
	assert.Equal(t, http.StatusOK, status, "a member the collection lists must be fetchable")

	status, _, _ = do(t, srv, http.MethodDelete, sessionPath, nil)
	require.Equal(t, http.StatusNoContent, status)

	status, collection, _ = do(t, srv, http.MethodGet, sessionsPath, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, collection["Members"].([]any), before,
		"a revoked session must leave the collection, or it grows without bound")
}
