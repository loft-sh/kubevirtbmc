package redfish

import (
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// The Dell job surface, as a client constructs it from the Manager's own
// @odata.id. Like DellAttributes, none of this is reachable by navigation, so
// the paths are derived from the manager id rather than hard-coded.
//
// A client clears the iDRAC job queue before submitting a new BIOS job. On
// real iDRAC that queue is real; here there is never anything in it, so the
// only correct answer is success. Answering 404 instead is read as the BMC
// failing mid-configuration, and the caller responds by force-restarting the
// host and trying again, which loops forever.
const (
	dellJobQueuePathTemplate   = "/redfish/v1/Managers/%s/Oem/Dell/DellJobService/Actions/DellJobService.DeleteJobQueue"
	dellJobServicePathTemplate = "/redfish/v1/Managers/%s/Oem/Dell/DellJobService"

	// Job creation and lookup are each served at two paths: clients differ on
	// whether the Oem/Dell segment is included, and real iDRAC honours both.
	dellJobsPathTemplate     = "/redfish/v1/Managers/%s/Jobs"
	dellOemJobsPathTemplate  = "/redfish/v1/Managers/%s/Oem/Dell/Jobs"
	dellJobPathTemplate      = "/redfish/v1/Managers/%s/Jobs/{jobId}"
	dellOemJobPathTemplate   = "/redfish/v1/Managers/%s/Oem/Dell/Jobs/{jobId}"
	dellImportConfigPathTmpl = "/redfish/v1/Managers/%s/Actions/Oem/EID_674_Manager.ImportSystemConfiguration"

	// The job type real iDRAC reports for a staged configuration change, and
	// the message id it pairs with it.
	dellJobType      = "DellConfiguration"
	dellJobMessageID = "PR19"
)

// dellJob is one entry in the emulated job queue.
type dellJob struct {
	id        string
	startTime time.Time
}

// dellJobService emulates the iDRAC job queue.
//
// Jobs are created already Completed. On real firmware a configuration job is
// Scheduled until the host power-cycles and applies it, and the reference mock
// models that, completing its jobs on power-on. There is no BIOS here to
// stage anything into, so the work is done the instant the job exists, and
// reporting Completed immediately lets a client's poll terminate on its first
// read whether or not it reboots the host first. Reporting Scheduled instead
// would hang any caller that polls before resetting.
type dellJobService struct {
	managerID string

	mu   sync.Mutex
	jobs map[string]dellJob
}

func newDellJobService(managerID string) *dellJobService {
	return &dellJobService{
		managerID: managerID,
		jobs:      map[string]dellJob{},
	}
}

// create registers a job and returns its id.
func (d *dellJobService) create() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var id string
	for {
		id = fmt.Sprintf("JID_%d", rand.Uint64())
		if _, taken := d.jobs[id]; !taken {
			break
		}
	}

	d.jobs[id] = dellJob{id: id, startTime: time.Now().UTC()}

	return id
}

func (d *dellJobService) get(id string) (dellJob, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	job, found := d.jobs[id]

	return job, found
}

// clear empties the queue. Nothing here needs cancelling, but a caller that
// clears the queue and then lists it should not still see its old jobs.
func (d *dellJobService) clear() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.jobs = map[string]dellJob{}
}

// jobPayload mirrors what real iDRAC reports for a completed job. The explicit
// nulls and TIME_NA are part of the shape, so this is written directly rather
// than through the response encoder that drops empty values.
func (d *dellJobService) jobPayload(job dellJob) map[string]any {
	startTime := job.startTime.Format(time.RFC3339)

	return map[string]any{
		"@odata.context":          "/redfish/v1/$metadata#DellJob.DellJob",
		"@odata.id":               fmt.Sprintf("/redfish/v1/Managers/%s/Oem/Dell/Jobs/%s", d.managerID, job.id),
		"@odata.type":             "#DellJob.v1_5_0.DellJob",
		"ActualRunningStartTime":  startTime,
		"ActualRunningStopTime":   nil,
		"CompletionTime":          nil,
		"Description":             "Job Instance",
		"EndTime":                 "TIME_NA",
		"Id":                      job.id,
		"JobState":                "Completed",
		"JobType":                 dellJobType,
		"Message":                 "Completed",
		"MessageArgs":             []any{},
		"MessageArgs@odata.count": 0,
		"MessageId":               dellJobMessageID,
		"Name":                    dellJobType,
		"PercentComplete":         100,
		"StartTime":               startTime,
		"TargetSettingsURI":       nil,
	}
}

func (d *dellJobService) serveDeleteJobQueue(w http.ResponseWriter, r *http.Request) {
	if !d.matchesManager(w, r) {
		return
	}

	// The request body is deliberately not read or validated. Real iDRAC takes
	// a JobID such as JID_CLEARALL or JID_CLEARALL_FORCE, and rejecting a
	// caller over the exact spelling would fail a step that is a no-op here.
	d.clear()

	logrus.Info("Cleared the emulated Dell job queue")

	writeJSON(w, http.StatusOK, map[string]any{})
}

// serveCreateJob answers a job submission with the Location header that points
// at the new job. Clients read Location to learn the job id, so a success
// without it leaves them with a job they cannot poll.
func (d *dellJobService) serveCreateJob(w http.ResponseWriter, r *http.Request) {
	if !d.matchesManager(w, r) {
		return
	}

	id := d.create()

	w.Header().Set("Location", fmt.Sprintf("/redfish/v1/Managers/%s/Jobs/%s", d.managerID, id))

	logrus.WithField("jobId", id).Info("Created an emulated Dell configuration job")

	writeJSON(w, http.StatusOK, map[string]any{})
}

func (d *dellJobService) serveGetJob(w http.ResponseWriter, r *http.Request) {
	if !d.matchesManager(w, r) {
		return
	}

	id := mux.Vars(r)["jobId"]

	job, found := d.get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    "Base.1.0.ResourceMissingAtURI",
				"message": fmt.Sprintf("could not find iDRAC job: %s", id),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, d.jobPayload(job))
}

// serveJobService advertises the actions this service supports.
//
// Clients construct the DeleteJobQueue URI rather than discovering it, so this
// resource is not strictly required. It is served anyway: it costs one route,
// and it means a client that does look the action up finds a target instead of
// a 404.
func (d *dellJobService) serveJobService(w http.ResponseWriter, r *http.Request) {
	if !d.matchesManager(w, r) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":   fmt.Sprintf(dellJobServicePathTemplate, d.managerID),
		"@odata.type": "#DellJobService.v1_0_0.DellJobService",
		"Id":          "DellJobService",
		"Name":        "Dell Job Service",
		"Description": "Dell Job Service",
		"Actions": map[string]any{
			"#DellJobService.DeleteJobQueue": map[string]any{
				"target": fmt.Sprintf(dellJobQueuePathTemplate, d.managerID),
			},
		},
	})
}

// matchesManager rejects a path built for some other manager id.
func (d *dellJobService) matchesManager(w http.ResponseWriter, r *http.Request) bool {
	if id := mux.Vars(r)["managerId"]; id != d.managerID {
		http.NotFound(w, r)
		return false
	}

	return true
}

// registerDellJobRoutes wires the Dell job surface for one manager.
func registerDellJobRoutes(router *mux.Router, authMiddleware mux.MiddlewareFunc, managerID string) {
	service := newDellJobService(managerID)

	jobs := router.NewRoute().Subrouter()
	jobs.Use(authMiddleware)

	jobs.Methods(http.MethodPost).
		Path(fmt.Sprintf(dellJobQueuePathTemplate, "{managerId}")).
		HandlerFunc(service.serveDeleteJobQueue)

	jobs.Methods(http.MethodGet).
		Path(fmt.Sprintf(dellJobServicePathTemplate, "{managerId}")).
		HandlerFunc(service.serveJobService)

	// Job submission. ImportSystemConfiguration stages a configuration the
	// same way a BIOS job does, and real iDRAC answers it with a job too.
	for _, path := range []string{
		fmt.Sprintf(dellJobsPathTemplate, "{managerId}"),
		fmt.Sprintf(dellOemJobsPathTemplate, "{managerId}"),
		fmt.Sprintf(dellImportConfigPathTmpl, "{managerId}"),
	} {
		jobs.Methods(http.MethodPost).Path(path).HandlerFunc(service.serveCreateJob)
	}

	for _, path := range []string{
		fmt.Sprintf(dellJobPathTemplate, "{managerId}"),
		fmt.Sprintf(dellOemJobPathTemplate, "{managerId}"),
	} {
		jobs.Methods(http.MethodGet).Path(path).HandlerFunc(service.serveGetJob)
	}

	logrus.WithField("deleteJobQueue", fmt.Sprintf(dellJobQueuePathTemplate, managerID)).
		Info("Serving the Dell job service")
}
