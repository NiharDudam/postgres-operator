package clusterservice

/*
Copyright 2017 Crunchy Data Solutions, Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import (
	"encoding/json"
	"net/http"
	"strconv"

	log "github.com/Sirupsen/logrus"
	"github.com/crunchydata/postgres-operator/apiserver"
	msgs "github.com/crunchydata/postgres-operator/apiservermsgs"
	"github.com/gorilla/mux"
)

// TestResults ...
type TestResults struct {
	Results []string
}

// ClusterDetail ...
type ClusterDetail struct {
	Name string
	//deployments
	//replicasets
	//pods
	//services
	//secrets
}

// CreateClusterHandler ...
// pgo create cluster
// parameters secretfrom
func CreateClusterHandler(w http.ResponseWriter, r *http.Request) {
	var ns string

	log.Debug("clusterservice.CreateClusterHandler called")
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
	username, err := apiserver.Authn(apiserver.CREATE_CLUSTER_PERM, w, r)
	if err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")

	var request msgs.CreateClusterRequest
	_ = json.NewDecoder(r.Body).Decode(&request)

	resp := msgs.CreateClusterResponse{}
	resp.Status = msgs.Status{Code: msgs.Ok, Msg: ""}

	if request.ClientVersion != msgs.PGO_VERSION {
		resp.Status.Code = msgs.Error
		resp.Status.Msg = apiserver.VERSION_MISMATCH_ERROR
		json.NewEncoder(w).Encode(resp)
		return
	}
	ns, err = apiserver.GetNamespace(username, request.Namespace)
	if err != nil {
		resp.Status.Code = msgs.Error
		resp.Status.Msg = err.Error()
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp = CreateCluster(&request, ns)
	json.NewEncoder(w).Encode(resp)

}

// ShowClusterHandler ...
// pgo show cluster
// pgo delete mycluster
// parameters showsecrets
// parameters selector
// parameters postgresversion
// returns a ShowClusterResponse
func ShowClusterHandler(w http.ResponseWriter, r *http.Request) {
	var ns string
	vars := mux.Vars(r)
	log.Debugf("clusterservice.ShowClusterHandler %v\n", vars)

	clustername := vars["name"]

	selector := r.URL.Query().Get("selector")
	ccpimagetag := r.URL.Query().Get("ccpimagetag")
	clientVersion := r.URL.Query().Get("version")
	namespace := r.URL.Query().Get("namespace")

	log.Debugf("ShowClusterHandler: parameters name [%s] selector [%s] ccpimagetag [%s] version [%s] namespace [%s]", clustername, selector, ccpimagetag, clientVersion, namespace)

	username, err := apiserver.Authn(apiserver.SHOW_CLUSTER_PERM, w, r)
	if err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

	log.Debug("clusterservice.ShowClusterHandler GET called")

	var resp msgs.ShowClusterResponse
	resp.Status = msgs.Status{Code: msgs.Ok, Msg: ""}

	if clientVersion != msgs.PGO_VERSION {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: apiserver.VERSION_MISMATCH_ERROR}
		resp.Results = make([]msgs.ShowClusterDetail, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}

	ns, err = apiserver.GetNamespace(username, namespace)
	if err != nil {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: err.Error()}
		resp.Results = make([]msgs.ShowClusterDetail, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp = ShowCluster(clustername, selector, ccpimagetag, ns)
	json.NewEncoder(w).Encode(resp)

}

// DeleteClusterHandler ...
// pgo delete mycluster
// parameters showsecrets
// parameters selector
// parameters postgresversion
// returns a ShowClusterResponse
func DeleteClusterHandler(w http.ResponseWriter, r *http.Request) {
	var ns string
	vars := mux.Vars(r)
	log.Debugf("clusterservice.DeleteClusterHandler %v\n", vars)

	clustername := vars["name"]

	selector := r.URL.Query().Get("selector")
	clientVersion := r.URL.Query().Get("version")
	namespace := r.URL.Query().Get("namespace")

	deleteData := false
	deleteDataStr := r.URL.Query().Get("delete-data")
	if deleteDataStr != "" {
		deleteData, _ = strconv.ParseBool(deleteDataStr)
	}
	deleteBackups := false
	deleteBackupsStr := r.URL.Query().Get("delete-backups")
	if deleteBackupsStr != "" {
		deleteBackups, _ = strconv.ParseBool(deleteBackupsStr)
	}

	log.Debugf("DeleteClusterHandler: parameters namespace [%s] selector [%s] delete-data [%s] delete-backups [%s]", namespace, selector, clientVersion, deleteDataStr, deleteBackupsStr)

	username, err := apiserver.Authn(apiserver.DELETE_CLUSTER_PERM, w, r)
	if err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

	log.Debug("clusterservice.DeleteClusterHandler called")

	resp := msgs.DeleteClusterResponse{}
	resp.Status = msgs.Status{Code: msgs.Ok, Msg: ""}

	if clientVersion != msgs.PGO_VERSION {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: apiserver.VERSION_MISMATCH_ERROR}
		resp.Results = make([]string, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}
	ns, err = apiserver.GetNamespace(username, namespace)
	if err != nil {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: err.Error()}
		resp.Results = make([]string, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp = DeleteCluster(clustername, selector, deleteData, deleteBackups, ns)
	json.NewEncoder(w).Encode(resp)

}

// TestClusterHandler ...
// pgo test mycluster
func TestClusterHandler(w http.ResponseWriter, r *http.Request) {
	var ns string
	vars := mux.Vars(r)
	clustername := vars["name"]

	selector := r.URL.Query().Get("selector")
	namespace := r.URL.Query().Get("namespace")
	clientVersion := r.URL.Query().Get("version")

	log.Debugf("TestClusterHandler parameters name [%s] version [%s] namespace [%s] selector [%s]", clustername, clientVersion, namespace, selector)

	username, err := apiserver.Authn(apiserver.TEST_CLUSTER_PERM, w, r)
	if err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

	resp := msgs.ClusterTestResponse{}
	resp.Status = msgs.Status{Code: msgs.Ok, Msg: ""}

	if clientVersion != msgs.PGO_VERSION {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: apiserver.VERSION_MISMATCH_ERROR}
		json.NewEncoder(w).Encode(resp)
		return
	}

	ns, err = apiserver.GetNamespace(username, namespace)
	if err != nil {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: err.Error()}
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp = TestCluster(clustername, selector, ns)
	json.NewEncoder(w).Encode(resp)
}

// UpdateClusterHandler ...
// pgo update cluster mycluster --autofail=true
// pgo update cluster --selector=env=research --autofail=false
// returns a UpdateClusterResponse
func UpdateClusterHandler(w http.ResponseWriter, r *http.Request) {
	var ns string
	vars := mux.Vars(r)

	clustername := vars["name"]

	selector := r.URL.Query().Get("selector")
	namespace := r.URL.Query().Get("namespace")
	clientVersion := r.URL.Query().Get("version")

	autofailStr := r.URL.Query().Get("autofail")

	log.Debugf("UpdateClusterHandler parameters name [%s] version [%s] selector [%s] namespace [%s] autofail [%s]", clustername, clientVersion, selector, namespace, autofailStr)

	username, err := apiserver.Authn(apiserver.UPDATE_CLUSTER_PERM, w, r)
	if err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

	log.Debug("clusterservice.UpdateClusterHandler called")

	resp := msgs.UpdateClusterResponse{}
	resp.Status = msgs.Status{Code: msgs.Ok, Msg: ""}

	if clientVersion != msgs.PGO_VERSION {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: apiserver.VERSION_MISMATCH_ERROR}
		resp.Results = make([]string, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}

	ns, err = apiserver.GetNamespace(username, namespace)
	if err != nil {
		resp.Status = msgs.Status{Code: msgs.Error, Msg: err.Error()}
		resp.Results = make([]string, 0)
		json.NewEncoder(w).Encode(resp)
		return
	}

	if autofailStr != "" {
		if autofailStr == "true" || autofailStr == "false" {
		} else {
			resp.Status = msgs.Status{
				Code: msgs.Error,
				Msg:  "autofail parameter is not true or false, boolean is required"}
			resp.Results = make([]string, 0)
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	resp = UpdateCluster(clustername, selector, autofailStr, ns)
	json.NewEncoder(w).Encode(resp)

}
