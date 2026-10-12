package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"kubevirt.io/kubevirtbmc/pkg/session"
)

// The AccountService exposes a single administrator account whose id is its
// username (libredfish's DPU client PATCHes Accounts/<username>, the others look
// the id up by username). A password change is stored as a hash on the VM and
// replaces the password the BMC was started with.
const (
	accountServicePath = "/redfish/v1/AccountService"
	accountsPath       = accountServicePath + "/Accounts"
)

type odataRef struct {
	ID string `json:"@odata.id"`
}

type accountServiceResource struct {
	ID                              string   `json:"@odata.id"`
	Type                            string   `json:"@odata.type"`
	ServiceID                       string   `json:"Id"`
	Name                            string   `json:"Name"`
	Accounts                        odataRef `json:"Accounts"`
	AccountLockoutCounterResetAfter int      `json:"AccountLockoutCounterResetAfter"`
	AccountLockoutDuration          int      `json:"AccountLockoutDuration"`
	AccountLockoutThreshold         int      `json:"AccountLockoutThreshold"`
	AuthFailureLoggingThreshold     int      `json:"AuthFailureLoggingThreshold"`
	LocalAccountAuth                string   `json:"LocalAccountAuth"`
	MaxPasswordLength               int      `json:"MaxPasswordLength"`
	MinPasswordLength               int      `json:"MinPasswordLength"`
}

type accountCollectionResource struct {
	ID      string     `json:"@odata.id"`
	Type    string     `json:"@odata.type"`
	Name    string     `json:"Name"`
	Members []odataRef `json:"Members"`
	Count   int        `json:"Members@odata.count"`
}

type accountResource struct {
	ID           string   `json:"@odata.id"`
	Type         string   `json:"@odata.type"`
	Name         string   `json:"Name"`
	AccountID    string   `json:"Id"`
	UserName     string   `json:"UserName"`
	RoleID       string   `json:"RoleId"`
	AccountTypes []string `json:"AccountTypes"`
}

func (h *handler) HandleAccountService(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, accountServiceResource{
		ID:                          accountServicePath,
		Type:                        "#AccountService.v1_9_0.AccountService",
		ServiceID:                   "AccountService",
		Name:                        "Account Service",
		Accounts:                    odataRef{ID: accountsPath},
		AuthFailureLoggingThreshold: 2,
		LocalAccountAuth:            "Fallback",
		MaxPasswordLength:           40,
	})
}

// HandleAccountServicePatch acknowledges the lockout policy writes; the shim
// enforces no lockout.
func (h *handler) HandleAccountServicePatch(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) HandleAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, accountCollectionResource{
		ID:      accountsPath,
		Type:    "#ManagerAccountCollection.ManagerAccountCollection",
		Name:    "Accounts Collection",
		Members: []odataRef{{ID: fmt.Sprintf("%s/%s", accountsPath, h.bmcUser)}},
		Count:   1,
	})
}

func (h *handler) HandleAccount(w http.ResponseWriter, r *http.Request) {
	if mux.Vars(r)["AccountId"] != h.bmcUser {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, accountResource{
		ID:           fmt.Sprintf("%s/%s", accountsPath, h.bmcUser),
		Type:         "#ManagerAccount.v1_8_0.ManagerAccount",
		Name:         "User Account",
		AccountID:    h.bmcUser,
		UserName:     h.bmcUser,
		RoleID:       "Administrator",
		AccountTypes: []string{"Redfish"},
	})
}

// HandleAccountPatch changes the account's password.
func (h *handler) HandleAccountPatch(w http.ResponseWriter, r *http.Request) {
	if mux.Vars(r)["AccountId"] != h.bmcUser {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		Password string `json:"Password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		http.Error(w, "Password must be a non-empty string", http.StatusBadRequest)
		return
	}

	hash, err := session.HashPassword(body.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.rm.SetPasswordHash(hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
