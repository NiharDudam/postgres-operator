package pvcservice

/*
Copyright 2017-2019 Crunchy Data Solutions, Inc.
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
	"bytes"
	"encoding/json"
	log "github.com/Sirupsen/logrus"
	"github.com/crunchydata/postgres-operator/apiserver"
	//"github.com/crunchydata/postgres-operator/config"
	"github.com/crunchydata/postgres-operator/kubeapi"
	"github.com/crunchydata/postgres-operator/util"
	"io"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"time"
)

type lspvcTemplateFields struct {
	Name               string
	ClusterName        string
	COImagePrefix      string
	COImageTag         string
	BackupRoot         string
	PVCName            string
	ContainerResources string
}

type containerResourcesTemplateFields struct {
	RequestsMemory, RequestsCPU string
	LimitsMemory, LimitsCPU     string
}

// ShowPVC ...
func ShowPVC(pvcName, PVCRoot, ns string) ([]string, error) {
	pvcList := make([]string, 1)

	if pvcName == "all" {
		selector := util.LABEL_PGREMOVE + "=true"

		pvcs, err := kubeapi.GetPVCs(apiserver.Clientset, selector, ns)
		if err != nil {
			return pvcList, err
		}

		log.Debugf("got %d PVCs from ShowPVC query", len(pvcs.Items))
		for _, p := range pvcs.Items {
			pvcList = append(pvcList, p.Name)
		}
		return pvcList, err
	}

	pvc, _, err := kubeapi.GetPVC(apiserver.Clientset, pvcName, ns)
	if err != nil {
		return pvcList, err
	}

	log.Debugf("PVC %s is found", pvc.Name)
	pvcList, err = printPVCListing(pvc.ObjectMeta.Labels[util.LABEL_PG_CLUSTER], pvc.Name, PVCRoot, ns)

	return pvcList, err

}

// printPVCListing ...
func printPVCListing(clusterName, pvcName, PVCRoot, ns string) ([]string, error) {
	newlines := make([]string, 1)
	var err error
	var doc2 bytes.Buffer
	var podName = "lspvc-" + pvcName

	//delete lspvc pod if it was not deleted for any reason prior
	_, found, err := kubeapi.GetPod(apiserver.Clientset, podName, ns)
	if !found {
		//
	} else if err != nil {
		log.Error(err.Error())
		return newlines, err
	} else {
		log.Debugf("deleting prior pod %s", podName)
		err = kubeapi.DeletePod(apiserver.Clientset, podName, ns)
		if err != nil {
			return newlines, err
		}
		//sleep a bit for the pod to be deleted
		for i := 0; i < 9; i++ {
			time.Sleep(2000 * time.Millisecond)
			_, found, err := kubeapi.GetPod(apiserver.Clientset, podName, ns)
			if !found || err != nil {
				break
			}
		}
	}

	pvcRoot := "/"
	if PVCRoot != "" {
		log.Debugf("using %s as the PVC listing root", PVCRoot)
		pvcRoot = PVCRoot
		log.Debugf("%s/%s", pvcName, pvcRoot)
	} else {
		log.Debug(pvcName)
	}

	cr := ""
	if apiserver.Pgo.DefaultLspvcResources != "" {
		tmp, err := apiserver.Pgo.GetContainerResource(apiserver.Pgo.DefaultLspvcResources)
		if err != nil {
			log.Error(err.Error())
			return newlines, err
		}
		cr = apiserver.GetContainerResourcesJSON(&tmp)

	}

	pvcFields := lspvcTemplateFields{
		Name:               podName,
		ClusterName:        clusterName,
		COImagePrefix:      apiserver.Pgo.Pgo.COImagePrefix,
		COImageTag:         apiserver.Pgo.Pgo.COImageTag,
		BackupRoot:         pvcRoot,
		PVCName:            pvcName,
		ContainerResources: cr,
	}

	err = apiserver.LspvcTemplate.Execute(&doc2, pvcFields)
	if err != nil {
		log.Error(err.Error())
		return newlines, err
	}
	docString := doc2.String()
	log.Debug(docString)

	//create lspvc pod
	newpod := v1.Pod{}
	err = json.Unmarshal(doc2.Bytes(), &newpod)
	if err != nil {
		log.Error("error unmarshalling json into Pod " + err.Error())
		return newlines, err
	}

	_, err = kubeapi.CreatePod(apiserver.Clientset, &newpod, ns)
	if err != nil {
		return newlines, err
	}

	timeout := time.Duration(6 * time.Second)
	lo := meta_v1.ListOptions{LabelSelector: "name=lspvc," + util.LABEL_PVCNAME + "=" + pvcName}
	podPhase := v1.PodSucceeded
	err = util.WaitUntilPod(apiserver.Clientset, lo, podPhase, timeout, ns)
	if err != nil {
		log.Error("error waiting on lspvc pod to complete" + err.Error())
	}

	time.Sleep(5000 * time.Millisecond)

	//get lspvc pod output
	logOptions := v1.PodLogOptions{}
	req := apiserver.Clientset.CoreV1().Pods(ns).GetLogs(podName, &logOptions)
	if req == nil {
		log.Debugf("error in get logs for %s", podName)
	} else {
		log.Debugf("got the logs for %s", podName)
	}
	readCloser, err := req.Stream()
	if err != nil {
		log.Error(err.Error())
		return newlines, err
	}

	defer func() {
		if readCloser != nil {
			readCloser.Close()
		}
	}()
	var buf2 bytes.Buffer
	_, err = io.Copy(&buf2, readCloser)
	log.Debugf("backups are... %s", buf2.String())

	log.Debugf("pvc = %s", pvcName)
	lines := strings.Split(buf2.String(), "\n")
	//chop off last line since its only a newline
	last := len(lines) - 1
	newlines = make([]string, last)
	copy(newlines, lines[:last])

	for k, v := range newlines {
		if k == len(newlines)-1 {
			log.Debugf("%s%s", apiserver.TreeTrunk, "/"+v)
		} else {
			log.Debugf("%s%s", apiserver.TreeBranch, "/"+v)
		}
	}

	//delete lspvc pod
	err = kubeapi.DeletePod(apiserver.Clientset, podName, ns)
	return newlines, err

}
