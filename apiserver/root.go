package apiserver

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
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"

	log "github.com/Sirupsen/logrus"
	crv1 "github.com/crunchydata/postgres-operator/apis/cr/v1"
	"github.com/crunchydata/postgres-operator/config"
	"github.com/crunchydata/postgres-operator/kubeapi"
	"github.com/crunchydata/postgres-operator/util"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const lspvcTemplatePath = "/pgo-config/pgo.lspvc-template.json"
const containerResourcesTemplatePath = "/pgo-config/container-resources.json"

// pgouserPath ...
const pgouserPath = "/pgo-auth-secret/pgouser"

const VERSION_MISMATCH_ERROR = "pgo client and server version mismatch"

// RESTClient ...
var RESTClient *rest.RESTClient

// Clientset ...
var Clientset *kubernetes.Clientset
var RESTConfig *rest.Config

// MetricsFlag if set to true will cause crunchy-collect to be added into new clusters
var MetricsFlag, BadgerFlag bool

// AuditFlag if set to true will cause auditing to occur in the logs
var AuditFlag bool

// DebugFlag is the debug flag value
var DebugFlag bool

// BasicAuth comes from the apiserver config
var BasicAuth bool

// Namespace comes from the apiserver config in this version
var Namespace string

// TreeTrunk is for debugging only in this context
const TreeTrunk = "└── "

// TreeBranch is for debugging only in this context
const TreeBranch = "├── "

type CredentialDetail struct {
	Username string
	Password string
	Role     string
}

// Credentials holds the BasicAuth credentials found in the config
var Credentials map[string]CredentialDetail

var ContainerResourcesTemplate *template.Template
var LoadTemplate *template.Template
var LspvcTemplate *template.Template
var JobTemplate *template.Template

var Pgo config.PgoConfig

type containerResourcesTemplateFields struct {
	RequestsMemory, RequestsCPU string
	LimitsMemory, LimitsCPU     string
}

func Initialize() {

	Pgo.GetConf()
	log.Println("CCPImageTag=" + Pgo.Cluster.CCPImageTag)
	log.Println("PrimaryNodeLabel=" + Pgo.Cluster.PrimaryNodeLabel)
	err := Pgo.Validate()
	if err != nil {
		log.Error(err)
		log.Error("something did not validate in the pgo.yaml")
		os.Exit(2)
	}

	Namespace = os.Getenv("NAMESPACE")
	if Namespace == "" {
		log.Error("NAMESPACE environment variable is required")
		os.Exit(2)
	}
	log.Info("Namespace is [" + Namespace + "]")
	BasicAuth = true
	MetricsFlag = false
	BadgerFlag = false
	AuditFlag = false

	log.Infoln("apiserver starts")

	getCredentials()
	initConfig()

	initTemplates()

	InitializePerms()

	err = validateCredentials()
	if err != nil {
		os.Exit(2)
	}

	ConnectToKube()

	validateWithKube()
}

// ConnectToKube ...
func ConnectToKube() {

	kubeconfig := flag.String("kubeconfig", "", "Path to a kube config. Only required if out-of-cluster.")
	flag.Parse()

	var err error
	RESTConfig, err = buildConfig(*kubeconfig)
	if err != nil {
		panic(err)
	}

	Clientset, err = kubernetes.NewForConfig(RESTConfig)
	if err != nil {
		panic(err)
	}

	// make a new config for our extension's API group, using the first config as a baseline
	//RESTClient, _, err = crdclient.NewClient(RESTConfig)
	RESTClient, _, err = util.NewClient(RESTConfig)
	if err != nil {
		panic(err)
	}

}

// buildConfig ...
func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func initConfig() {

	AuditFlag = Pgo.Pgo.Audit
	if AuditFlag {
		log.Info("audit flag is set to true")
	}

	MetricsFlag = Pgo.Cluster.Metrics
	if MetricsFlag {
		log.Info("metrics flag is set to true")
	}
	BadgerFlag = Pgo.Cluster.Badger
	if BadgerFlag {
		log.Info("badger flag is set to true")
	}

	tmp := Pgo.BasicAuth
	if tmp == "" {
		BasicAuth = true
	} else {
		var err error
		BasicAuth, err = strconv.ParseBool(tmp)
		if err != nil {
			log.Error("BasicAuth config value is not valid")
			os.Exit(2)
		}
	}
	log.Infof("BasicAuth is %v", BasicAuth)

	if !validStorageSettings() {
		log.Error("Storage Settings are not defined correctly, can't continue")
		os.Exit(2)
	}

	if !validContainerResourcesSettings() {
		log.Error("Container Resources settings are not defined correctly, can't continue")
		os.Exit(2)
	}

}

func file2lines(filePath string) []string {
	f, err := os.Open(filePath)
	if err != nil {
		log.Error(err)
		os.Exit(2)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Error(err)
	}

	return lines
}

func parseUserMap(dat string) CredentialDetail {

	creds := CredentialDetail{}

	fields := strings.Split(strings.TrimSpace(dat), ":")
	creds.Username = fields[0]
	creds.Password = fields[1]
	creds.Role = fields[2]
	return creds
}

// getCredentials ...
func getCredentials() {

	Credentials = make(map[string]CredentialDetail)

	lines := file2lines(pgouserPath)
	for _, v := range lines {
		creds := parseUserMap(v)
		Credentials[creds.Username] = creds
	}
	log.Debugf("pgouser has %v", Credentials)

}

// validateCredentials ...
func validateCredentials() error {

	var err error

	for _, v := range Credentials {
		log.Infof("validating user %s and role %s", v.Username, v.Role)
		if RoleMap[v.Role] == nil {
			errMsg := fmt.Sprintf("role not found on pgouser user [%s], invalid role was [%s]", v.Username, v.Role)
			log.Error(errMsg)
			return errors.New(errMsg)
		}
	}

	return err
}

func BasicAuthCheck(username, password string) bool {

	if BasicAuth == false {
		return true
	}

	value := Credentials[username]
	if (CredentialDetail{}) == value {
		return false
	}

	if value.Password != password {
		return false
	}

	return true
}

func BasicAuthzCheck(username, perm string) bool {

	creds := Credentials[username]
	if creds == (CredentialDetail{}) {
		//this means username not found in pgouser file
		//should not happen at this point in code!
		log.Error("%s not found in pgouser file", username)
		return false
	}

	log.Infof("BasicAuthzCheck %s %s %v", creds.Role, perm, HasPerm(creds.Role, perm))
	return HasPerm(creds.Role, perm)

}

//GetNamespace determines if a user has permission for
//a namespace they are requesting as well as looks up
//a default namespace if the requestedNS is empty
func GetNamespace(username, requestedNS string) (string, error) {
	var err error

	log.Debugf("GetNamespace username [%s] ns [%s]", username, requestedNS)

	return Namespace, err
}

func Authn(perm string, w http.ResponseWriter, r *http.Request) (string, error) {
	var err error
	w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

	username, password, authOK := r.BasicAuth()
	if AuditFlag {
		log.Infof("[audit] %s username=[%s] method=[%s] ip=[%s]", perm, username, r.Method, r.RemoteAddr)
	}

	log.Debugf("Authentication Attempt %s username=[%s]", perm, username)
	if authOK == false {
		http.Error(w, "Not authorized", 401)
		return "", errors.New("Not Authorized")
	}

	if !BasicAuthCheck(username, password) {
		log.Errorf("Authentication Failed %s username=[%s]", perm, username)
		http.Error(w, "Not authenticated in apiserver", 401)
		return "", errors.New("Not Authenticated")
	}

	if !BasicAuthzCheck(username, perm) {
		log.Errorf("Authentication Failed %s username=[%s]", perm, username)
		http.Error(w, "Not authorized for this apiserver action", 401)
		return "", errors.New("Not authorized for this apiserver action")
	}

	log.Debug("Authentication Success")
	return username, err

}

func validContainerResourcesSettings() bool {
	log.Infof("ContainerResources has %d definitions", len(Pgo.ContainerResources))

	//validate any Container Resources in pgo.yaml for correct formats
	if !IsValidContainerResourceValues() {
		return false
	}

	drs := Pgo.DefaultContainerResources
	if drs == "" {
		log.Info("DefaultContainerResources was not specified in pgo.yaml, so no container resources will be specified")
		return true
	}

	//validate the DefaultContainerResource value
	if IsValidContainerResource(drs) {
		log.Info(drs + " is valid")
	} else {
		log.Error(drs + " is NOT valid")
		return false
	}

	return true

}

func validStorageSettings() bool {
	log.Infof("Storage has %d definitions", len(Pgo.Storage))

	ps := Pgo.PrimaryStorage
	if IsValidStorageName(ps) {
		log.Info(ps + " is valid")
	} else {
		log.Error(ps + " is NOT valid")
		return false
	}
	rs := Pgo.ReplicaStorage
	if IsValidStorageName(rs) {
		log.Info(rs + " is valid")
	} else {
		log.Error(rs + " is NOT valid")
		return false
	}
	bs := Pgo.BackupStorage
	if IsValidStorageName(bs) {
		log.Info(bs + " is valid")
	} else {
		log.Error(bs + " is NOT valid")
		return false
	}

	return true

}

func IsValidContainerResource(name string) bool {
	_, ok := Pgo.ContainerResources[name]
	return ok
}

func IsValidStorageName(name string) bool {
	_, ok := Pgo.Storage[name]
	return ok
}

// ValidateNodeLabel
// returns error if node label is invalid
func ValidateNodeLabel(nodeLabel string) error {
	parts := strings.Split(nodeLabel, "=")
	if len(parts) != 2 {
		return errors.New(nodeLabel + " node label does not follow key=value format")
	}

	keyValid, valueValid, err := IsValidNodeLabel(parts[0], parts[1])
	if err != nil {
		return err
	}

	if !keyValid {
		return errors.New(nodeLabel + " key was not valid .. check node labels for correct values to specify")
	}
	if !valueValid {
		return errors.New(nodeLabel + " node label value was not valid .. check node labels for correct values to specify")
	}

	return nil
}

// IsValidNodeLabel
// returns bool for key validity
// returns bool for value validity
// returns error
func IsValidNodeLabel(key, value string) (bool, bool, error) {

	var err error
	keyValid := false
	valueValid := false

	nodes, err := kubeapi.GetAllNodes(Clientset)
	if err != nil {
		return false, false, err
	}

	for _, node := range nodes.Items {

		if val, exists := node.ObjectMeta.Labels[key]; exists {
			keyValid = true
			if val == value {
				valueValid = true
			}
		}
	}

	return keyValid, valueValid, err
}

func IsValidContainerResourceValues() bool {

	var err error

	for k, v := range Pgo.ContainerResources {
		log.Infof("Container Resources %s [%v]", k, v)
		resources, _ := Pgo.GetContainerResource(k)
		_, err = resource.ParseQuantity(resources.RequestsMemory)
		if err != nil {
			log.Errorf("%s.RequestsMemory value invalid format", k)
			return false
		}
		_, err = resource.ParseQuantity(resources.RequestsCPU)
		if err != nil {
			log.Errorf("%s.RequestsCPU value invalid format", k)
			return false
		}
		_, err = resource.ParseQuantity(resources.LimitsMemory)
		if err != nil {
			log.Errorf("%s.LimitsMemory value invalid format", k)
			return false
		}
		_, err = resource.ParseQuantity(resources.LimitsCPU)
		if err != nil {
			log.Errorf("%s.LimitsCPU value invalid format", k)
			return false
		}
	}
	return true
}

func initTemplates() {
	LspvcTemplate = util.LoadTemplate(lspvcTemplatePath)

	LoadTemplatePath := Pgo.Pgo.LoadTemplate
	if LoadTemplatePath == "" {
		log.Error("Pgo.LoadTemplate not defined in pgo config 1.")
		os.Exit(2)
	}

	JobTemplate = util.LoadTemplate(LoadTemplatePath)

	ContainerResourcesTemplate = util.LoadTemplate(containerResourcesTemplatePath)

}

func validateWithKube() {
	log.Debug("validateWithKube called")

	configNodeLabels := make([]string, 2)
	configNodeLabels[0] = Pgo.Cluster.PrimaryNodeLabel
	configNodeLabels[1] = Pgo.Cluster.ReplicaNodeLabel

	for _, n := range configNodeLabels {

		//parse & validate pgo.yaml node labels if set
		if n != "" {

			if err := ValidateNodeLabel(n); err != nil {
				log.Error(n + " node label specified in pgo.yaml is invalid")
				log.Error(err)
				os.Exit(2)
			}

			log.Debugf("%s is a valid pgo.yaml node label default", n)
		}
	}
}

// GetContainerResources ...
func GetContainerResourcesJSON(resources *crv1.PgContainerResources) string {

	//test for the case where no container resources are specified
	if resources.RequestsMemory == "" || resources.RequestsCPU == "" ||
		resources.LimitsMemory == "" || resources.LimitsCPU == "" {
		return ""
	}
	fields := containerResourcesTemplateFields{}
	fields.RequestsMemory = resources.RequestsMemory
	fields.RequestsCPU = resources.RequestsCPU
	fields.LimitsMemory = resources.LimitsMemory
	fields.LimitsCPU = resources.LimitsCPU

	doc := bytes.Buffer{}
	err := ContainerResourcesTemplate.Execute(&doc, fields)
	if err != nil {
		log.Error(err.Error())
		return ""
	}

	if log.GetLevel() == log.DebugLevel {
		ContainerResourcesTemplate.Execute(os.Stdout, fields)
	}

	return doc.String()
}
