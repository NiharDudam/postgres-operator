package controller

/*
Copyright 2019 Crunchy Data Solutions, Inc.
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
	"context"
	log "github.com/Sirupsen/logrus"
	crv1 "github.com/crunchydata/postgres-operator/apis/cr/v1"
	"github.com/crunchydata/postgres-operator/kubeapi"
	backrestoperator "github.com/crunchydata/postgres-operator/operator/backrest"
	clusteroperator "github.com/crunchydata/postgres-operator/operator/cluster"
	taskoperator "github.com/crunchydata/postgres-operator/operator/task"
	"github.com/crunchydata/postgres-operator/util"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// PodController holds the connections for the controller
type PodController struct {
	PodClient    *rest.RESTClient
	PodClientset *kubernetes.Clientset
	Namespace    string
}

// Run starts an pod resource controller
func (c *PodController) Run(ctx context.Context) error {

	_, err := c.watchPods(ctx)
	if err != nil {
		log.Errorf("Failed to register watch for pod resource: %v", err)
		return err
	}

	<-ctx.Done()
	return ctx.Err()
}

// watchPods is the event loop for pod resources
func (c *PodController) watchPods(ctx context.Context) (cache.Controller, error) {
	source := cache.NewListWatchFromClient(
		c.PodClientset.CoreV1().RESTClient(),
		"pods",
		c.Namespace,
		fields.Everything())

	_, controller := cache.NewInformer(
		source,

		// The object type.
		&apiv1.Pod{},

		// resyncPeriod
		// Every resyncPeriod, all resources in the cache will retrigger events.
		// Set to 0 to disable the resync.
		0,

		// Your custom resource event handlers.
		cache.ResourceEventHandlerFuncs{
			AddFunc:    c.onAdd,
			UpdateFunc: c.onUpdate,
			DeleteFunc: c.onDelete,
		})

	go controller.Run(ctx.Done())
	return controller, nil
}

// onAdd is called when a pgcluster is added or
// if a pgo-backrest-repo pod is added
func (c *PodController) onAdd(obj interface{}) {
	newpod := obj.(*apiv1.Pod)
	log.Debugf("[PodController] OnAdd ns=%s %s", newpod.ObjectMeta.Namespace, newpod.ObjectMeta.SelfLink)

	//handle the case when a pg database pod is added
	if isPostgresPod(newpod) {
		c.checkPostgresPods(newpod, newpod.ObjectMeta.Namespace)
		return
	}
}

// onUpdate is called when a pgcluster is updated
func (c *PodController) onUpdate(oldObj, newObj interface{}) {
	oldpod := oldObj.(*apiv1.Pod)
	newpod := newObj.(*apiv1.Pod)
	log.Debugf("[PodController] onUpdate ns=%s %s", newpod.ObjectMeta.Namespace, newpod.ObjectMeta.SelfLink)

	//handle the case when a pg database pod is updated
	if isPostgresPod(newpod) {
		c.checkReadyStatus(oldpod, newpod)
		return
	}
}

// onDelete is called when a pgcluster is deleted
func (c *PodController) onDelete(obj interface{}) {
	pod := obj.(*apiv1.Pod)
	log.Debugf("[PodController] onDelete ns=%s %s", pod.ObjectMeta.Namespace, pod.ObjectMeta.SelfLink)
}

func (c *PodController) checkReadyStatus(oldpod, newpod *apiv1.Pod) {
	//handle the case of a service-name re-label
	if newpod.ObjectMeta.Labels[util.LABEL_SERVICE_NAME] !=
		oldpod.ObjectMeta.Labels[util.LABEL_SERVICE_NAME] {
		log.Debug("the pod was updated and the service names were changed in this pod update, not going to check the ReadyStatus")
		return
	}
	//handle the case of a database pod going to Ready that has
	//autofail enabled
	autofailEnabled := c.checkAutofailLabel(newpod, newpod.ObjectMeta.Namespace)
	clusterName := newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER]
	if newpod.ObjectMeta.Labels[util.LABEL_SERVICE_NAME] == clusterName &&
		clusterName != "" && autofailEnabled {
		log.Debugf("autofail pg-cluster %s updated!", clusterName)
		for _, v := range newpod.Status.ContainerStatuses {
			if v.Name == "database" {
				clusteroperator.AutofailBase(c.PodClientset, c.PodClient, v.Ready, clusterName, newpod.ObjectMeta.Namespace)
			}
		}
	}

	//handle applying policies, and updating workflow after a database  pod
	//is made Ready, in the case of backrest, create the create stanza job
	if newpod.ObjectMeta.Labels[util.LABEL_SERVICE_NAME] == clusterName {
		var oldDatabaseStatus bool
		for _, v := range oldpod.Status.ContainerStatuses {
			if v.Name == "database" {
				oldDatabaseStatus = v.Ready
			}
		}
		for _, v := range newpod.Status.ContainerStatuses {
			if v.Name == "database" {
				//see if there are pgtasks for adding a policy
				if oldDatabaseStatus == false && v.Ready {
					log.Debugf("%s went to Ready from Not Ready, apply policies...", clusterName)
					taskoperator.ApplyPolicies(clusterName, c.PodClientset, c.PodClient, newpod.ObjectMeta.Namespace)
					taskoperator.CompleteCreateClusterWorkflow(clusterName, c.PodClientset, c.PodClient, newpod.ObjectMeta.Namespace)
					if newpod.ObjectMeta.Labels[util.LABEL_BACKREST] == "true" {
						tmptask := crv1.Pgtask{}
						found, err := kubeapi.Getpgtask(c.PodClient, &tmptask, clusterName+"-stanza-create", newpod.ObjectMeta.Namespace)
						if !found && err != nil {
							backrestoperator.StanzaCreate(newpod.ObjectMeta.Namespace, clusterName, c.PodClientset, c.PodClient)
						}
					}
				}
			}
		}
	}

}

// checkPostgresPods
// see if this is a primary or replica being created
// update service-name label on the pod for each case
// to match the correct Service selector for the PG cluster
func (c *PodController) checkPostgresPods(newpod *apiv1.Pod, ns string) {

	var dep *v1beta1.Deployment
	dep, _, err := kubeapi.GetDeployment(c.PodClientset, newpod.ObjectMeta.Labels[util.LABEL_DEPLOYMENT_NAME], ns)
	if err != nil {
		log.Errorf("could not get Deployment on pod Add %s", newpod.Name)
		return
	}

	if newpod.ObjectMeta.Labels[util.LABEL_PRIMARY] == "true" || newpod.ObjectMeta.Labels[util.LABEL_PRIMARY] == "false" {

		serviceName := ""

		if dep.ObjectMeta.Labels[util.LABEL_SERVICE_NAME] != "" {
			log.Debug("this means the deployment was already labeled")
			log.Debug("which means its pod was restarted for some reason")
			log.Debug("we will use the service name on the deployment")
			serviceName = dep.ObjectMeta.Labels[util.LABEL_SERVICE_NAME]
		} else if newpod.ObjectMeta.Labels[util.LABEL_PRIMARY] == "true" {
			log.Debugf("primary pod ADDED %s service-name=%s", newpod.Name, newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER])
			//add label onto pod "service-name=clustername"
			serviceName = newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER]
		} else if newpod.ObjectMeta.Labels[util.LABEL_PRIMARY] == "false" {
			log.Debugf("replica pod ADDED %s service-name=%s", newpod.Name, newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER]+"-replica")
			//add label onto pod "service-name=clustername-replica"
			serviceName = newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER] + "-replica"
		}

		err = kubeapi.AddLabelToPod(c.PodClientset, newpod, util.LABEL_SERVICE_NAME, serviceName, ns)
		if err != nil {
			log.Error(err)
			log.Errorf(" could not add pod label for pod %s and label %s ...", newpod.Name, serviceName)
			return
		}

		//add the service name label to the Deployment
		err = kubeapi.AddLabelToDeployment(c.PodClientset, dep, util.LABEL_SERVICE_NAME, serviceName, ns)

		if err != nil {
			log.Error("could not add label to deployment on pod add")
			return
		}

	}

}

//check for the autofail flag on the pgcluster CRD
func (c *PodController) checkAutofailLabel(newpod *apiv1.Pod, ns string) bool {
	clusterName := newpod.ObjectMeta.Labels[util.LABEL_PG_CLUSTER]

	pgcluster := crv1.Pgcluster{}
	found, err := kubeapi.Getpgcluster(c.PodClient, &pgcluster, clusterName, ns)
	if !found {
		return false
	} else if err != nil {
		log.Error(err)
		return false
	}

	if pgcluster.ObjectMeta.Labels[util.LABEL_AUTOFAIL] == "true" {
		log.Debugf("autofail is on for this pod %s", newpod.Name)
		return true
	}
	return false

}

func isPostgresPod(newpod *apiv1.Pod) bool {
	if newpod.ObjectMeta.Labels[util.LABEL_PGO_BACKREST_REPO] == "true" {
		log.Debugf("pgo-backrest-repo found %s", newpod.Name)
		return false
	}
	if newpod.ObjectMeta.Labels[util.LABEL_JOB_NAME] != "" {
		log.Debugf("job pod found [%s]", newpod.Name)
		return false
	}
	if newpod.ObjectMeta.Labels[util.LABEL_NAME] == "postgres-operator" {
		log.Debugf("postgres-operator-pod added [%s]", newpod.Name)
		return false
	}
	if newpod.ObjectMeta.Labels[util.LABEL_PGO_BACKREST_REPO] == "true" {
		log.Debugf("pgo-backrest-repo pod added [%s]", newpod.Name)
		return false
	}
	if newpod.ObjectMeta.Labels[util.LABEL_PGPOOL] == "true" {
		log.Debugf("pgpool pod added [%s]", newpod.Name)
		return false
	}
	if newpod.ObjectMeta.Labels[util.LABEL_PGBOUNCER] == "true" {
		log.Debugf("pgbouncer pod added [%s]", newpod.Name)
		return false
	}
	return true
}
