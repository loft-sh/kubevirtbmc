package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func dellJobRouter() *mux.Router {
	router := mux.NewRouter()
	registerDellJobRoutes(router, passThroughAuth, resourcemanager.DefaultManagerId)
	return router
}

func doJobRequest(t *testing.T, router *mux.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	return recorder
}

// The 404 that was force-restarting every machine in a loop. A client clears
// the iDRAC job queue before submitting a BIOS job; a failure there reads as
// the BMC dying mid-configuration, and the caller's remedy is to power-cycle
// the host and start over, forever. There is never anything in this queue, so
// success is the only correct answer.
func TestDellJobService_DeleteJobQueueSucceeds(t *testing.T) {
	router := dellJobRouter()

	recorder := doJobRequest(t, router, http.MethodPost,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue",
		`{"JobID":"JID_CLEARALL"}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.JSONEq(t, `{}`, recorder.Body.String())
}

// The body is not validated on purpose. Real iDRAC takes JID_CLEARALL and
// JID_CLEARALL_FORCE, and rejecting a caller over the exact spelling, or over
// sending nothing at all, would fail a step that is a no-op here.
func TestDellJobService_DeleteJobQueueIgnoresBody(t *testing.T) {
	router := dellJobRouter()

	for _, body := range []string{
		"",
		`{"JobID":"JID_CLEARALL_FORCE"}`,
		`{"JobID":"JID_CLEARALL"}`,
		`{"Unexpected":"shape"}`,
		`not json at all`,
	} {
		recorder := doJobRequest(t, router, http.MethodPost,
			"/redfish/v1/Managers/BMC/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue", body)
		assert.Equal(t, http.StatusOK, recorder.Code, "body %q must still succeed", body)
	}
}

// A submitted job must be pollable, and must report Completed so the poll
// terminates. Clients learn the job id from the Location header, so a success
// without one leaves them holding a job they cannot look up.
func TestDellJobService_CreateReturnsPollableCompletedJob(t *testing.T) {
	for _, createPath := range []string{
		"/redfish/v1/Managers/BMC/Jobs",
		"/redfish/v1/Managers/BMC/Oem/Dell/Jobs",
		"/redfish/v1/Managers/BMC/Actions/Oem/EID_674_Manager.ImportSystemConfiguration",
	} {
		t.Run(createPath, func(t *testing.T) {
			router := dellJobRouter()

			created := doJobRequest(t, router, http.MethodPost, createPath, `{"ShareParameters":{}}`)
			require.Equal(t, http.StatusOK, created.Code)

			location := created.Header().Get("Location")
			require.NotEmpty(t, location, "Location must carry the new job's URI")
			assert.Contains(t, location, "/redfish/v1/Managers/BMC/Jobs/JID_")

			fetched := doJobRequest(t, router, http.MethodGet, location, "")
			require.Equal(t, http.StatusOK, fetched.Code)

			var job map[string]any
			require.NoError(t, json.Unmarshal(fetched.Body.Bytes(), &job))

			// Completed on creation: there is no BIOS here to stage anything
			// into, so the work is done the moment the job exists. Reporting
			// Scheduled would hang a caller that polls before resetting.
			assert.Equal(t, "Completed", job["JobState"])
			assert.Equal(t, float64(100), job["PercentComplete"])
			assert.Equal(t, "DellConfiguration", job["JobType"])
			assert.Equal(t, "TIME_NA", job["EndTime"])
			assert.NotEmpty(t, job["Id"])
		})
	}
}

// The same job must be readable at both spellings: clients differ on whether
// the Oem/Dell segment is present, and real iDRAC honours both.
func TestDellJobService_JobReadableAtBothPaths(t *testing.T) {
	router := dellJobRouter()

	created := doJobRequest(t, router, http.MethodPost, "/redfish/v1/Managers/BMC/Jobs", "")
	require.Equal(t, http.StatusOK, created.Code)

	id := strings.TrimPrefix(created.Header().Get("Location"), "/redfish/v1/Managers/BMC/Jobs/")
	require.NotEmpty(t, id)

	for _, path := range []string{
		"/redfish/v1/Managers/BMC/Jobs/" + id,
		"/redfish/v1/Managers/BMC/Oem/Dell/Jobs/" + id,
	} {
		recorder := doJobRequest(t, router, http.MethodGet, path, "")
		assert.Equal(t, http.StatusOK, recorder.Code, "job must be readable at %s", path)
	}
}

// An unknown job is a 404 rather than a fabricated Completed job: inventing one
// would tell a client its submission succeeded when it never happened.
func TestDellJobService_UnknownJobIsNotFound(t *testing.T) {
	recorder := doJobRequest(t, dellJobRouter(), http.MethodGet, "/redfish/v1/Managers/BMC/Jobs/JID_does_not_exist", "")

	assert.Equal(t, http.StatusNotFound, recorder.Code)
}

// Clearing the queue must actually empty it, so a client that clears and then
// re-reads does not still see the old jobs.
func TestDellJobService_DeleteJobQueueEmptiesTheQueue(t *testing.T) {
	router := dellJobRouter()

	created := doJobRequest(t, router, http.MethodPost, "/redfish/v1/Managers/BMC/Jobs", "")
	location := created.Header().Get("Location")
	require.Equal(t, http.StatusOK, doJobRequest(t, router, http.MethodGet, location, "").Code)

	require.Equal(t, http.StatusOK, doJobRequest(t, router, http.MethodPost,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue", "").Code)

	assert.Equal(t, http.StatusNotFound, doJobRequest(t, router, http.MethodGet, location, "").Code,
		"a cleared queue must not still serve its old jobs")
}

// The service resource advertises the action with a target, so a client that
// looks it up rather than constructing it finds something real.
func TestDellJobService_AdvertisesDeleteJobQueueTarget(t *testing.T) {
	recorder := doJobRequest(t, dellJobRouter(), http.MethodGet, "/redfish/v1/Managers/BMC/Oem/Dell/DellJobService", "")
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Actions map[string]struct {
			Target string `json:"target"`
		} `json:"Actions"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	action, present := body.Actions["#DellJobService.DeleteJobQueue"]
	require.True(t, present, "the DeleteJobQueue action must be advertised")
	assert.Equal(t,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue",
		action.Target)
}

// Paths built for a different manager must not be served.
func TestDellJobService_RejectsOtherManagers(t *testing.T) {
	recorder := doJobRequest(t, dellJobRouter(), http.MethodPost,
		"/redfish/v1/Managers/iDRAC.Embedded.1/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue", "")

	assert.Equal(t, http.StatusNotFound, recorder.Code)
}
