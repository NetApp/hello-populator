/*
Copyright 2017 The Kubernetes Authors.
Copyright (c) 2020 NetApp

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

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	corev1 "k8s.io/api/core/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"

	hellov1 "github.com/kubernetes-csi/hello-populator/pkg/apis/hello/v1alpha1"
	helloclientset "github.com/kubernetes-csi/hello-populator/pkg/client/clientset/versioned"
	hellofactory "github.com/kubernetes-csi/hello-populator/pkg/client/informers/externalversions"
	helloinformers "github.com/kubernetes-csi/hello-populator/pkg/client/informers/externalversions/hello/v1alpha1"
	hellolisters "github.com/kubernetes-csi/hello-populator/pkg/client/listers/hello/v1alpha1"
)

const (
	workingNamespace  = "hello"
	annoPrefix        = "hello.k8s.io"
	targetLabel       = annoPrefix + "/target"
	populatedFromAnno = annoPrefix + "/populated-from"
	pvcFinalizer      = annoPrefix + "/populate-target-protection"
)

var version = "unknown"

func main() {
	var (
		mode         string
		fileName     string
		fileContents string
		masterURL    string
		kubeconfig   string
		imageName    string
		showVersion  bool
	)
	// Main arg
	flag.StringVar(&mode, "mode", "", "Mode to run in (controller, backup, restore)")
	// Populate args
	flag.StringVar(&fileName, "file-name", "", "File name to populate")
	flag.StringVar(&fileContents, "file-contents", "", "Contents to populate file with")
	// Controller args
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&imageName, "image-name", "", "Image to use for populating")
	flag.BoolVar(&showVersion, "version", false, "display the version string")
	flag.Parse()

	if showVersion {
		fmt.Println(os.Args[0], version)
		os.Exit(0)
	}

	switch mode {
	case "controller":
		runController(masterURL, kubeconfig, imageName)
	case "populate":
		populate(fileName, fileContents)
	default:
		panic(fmt.Errorf("Invalid mode: %s\n", mode))
	}
}

func populate(fileName, fileContents string) {
	if "" == fileName || "" == fileContents {
		panic("Missing required arg")
	}
	f, err := os.Create(fileName)
	if nil != err {
		panic(err)
	}
	defer f.Close()

	if !strings.HasSuffix(fileContents, "\n") {
		fileContents += "\n"
	}

	_, err = f.WriteString(fileContents)
	if nil != err {
		panic(err)
	}
}

func runController(masterURL, kubeconfig, imageName string) {
	stopCh := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		close(stopCh)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if nil != err {
		panic(err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if nil != err {
		panic(err)
	}

	helloClient, err := helloclientset.NewForConfig(cfg)
	if nil != err {
		panic(err)
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, time.Second*30)
	helloInformerFactory := hellofactory.NewSharedInformerFactory(helloClient, time.Second*30)

	controller := newController(kubeClient, helloClient, imageName,
		kubeInformerFactory.Core().V1().PersistentVolumeClaims(),
		kubeInformerFactory.Core().V1().PersistentVolumes(),
		kubeInformerFactory.Core().V1().Pods(),
		helloInformerFactory.Hello().V1alpha1().Hellos())

	kubeInformerFactory.Start(stopCh)
	helloInformerFactory.Start(stopCh)

	if err = controller.Run(stopCh); nil != err {
		panic(err)
	}
}

type empty struct{}

type stringSet struct {
	set map[string]empty
}

type controller struct {
	kubeclientset  kubernetes.Interface
	helloclientset helloclientset.Interface
	imageName      string
	pvcLister      corelisters.PersistentVolumeClaimLister
	pvcSynced      cache.InformerSynced
	pvLister       corelisters.PersistentVolumeLister
	pvSynced       cache.InformerSynced
	podLister      corelisters.PodLister
	podSynced      cache.InformerSynced
	helloLister    hellolisters.HelloLister
	helloSynced    cache.InformerSynced
	mu             sync.Mutex
	notifyMap      map[string]*stringSet
	cleanupMap     map[string]*stringSet
	workqueue      workqueue.RateLimitingInterface
}

func (c *controller) callMeLater(keyToCall, objType, namespace, name string) {
	var key string
	if 0 == len(namespace) {
		key = objType + "/" + name
	} else {
		key = objType + "/" + namespace + "/" + name
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.notifyMap[key]
	if nil == s {
		s = &stringSet{make(map[string]empty)}
		c.notifyMap[key] = s
	}
	s.set[keyToCall] = empty{}
	s = c.cleanupMap[keyToCall]
	if nil == s {
		s = &stringSet{make(map[string]empty)}
		c.cleanupMap[keyToCall] = s
	}
	s.set[key] = empty{}
}

func (c *controller) cleanupNofications(keyToCall string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.cleanupMap[keyToCall]
	if nil == s {
		return
	}
	for key := range s.set {
		t := c.notifyMap[key]
		if nil == t {
			continue
		}
		delete(t.set, keyToCall)
		if 0 == len(t.set) {
			delete(c.notifyMap, key)
		}
	}
}

func newController(
	kubeclientset kubernetes.Interface,
	helloclientset helloclientset.Interface,
	imageName string,
	pvcInformer coreinformers.PersistentVolumeClaimInformer,
	pvInformer coreinformers.PersistentVolumeInformer,
	podInformer coreinformers.PodInformer,
	helloInformer helloinformers.HelloInformer,
) *controller {

	controller := &controller{
		kubeclientset:  kubeclientset,
		helloclientset: helloclientset,
		imageName:      imageName,
		pvcLister:      pvcInformer.Lister(),
		pvcSynced:      pvcInformer.Informer().HasSynced,
		pvLister:       pvInformer.Lister(),
		pvSynced:       pvInformer.Informer().HasSynced,
		podLister:      podInformer.Lister(),
		podSynced:      podInformer.Informer().HasSynced,
		helloLister:    helloInformer.Lister(),
		helloSynced:    helloInformer.Informer().HasSynced,
		notifyMap:      make(map[string]*stringSet),
		cleanupMap:     make(map[string]*stringSet),
		workqueue:      workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}

	pvcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handlePVC,
		UpdateFunc: func(old, new interface{}) {
			newPvc := new.(*corev1.PersistentVolumeClaim)
			oldPvc := old.(*corev1.PersistentVolumeClaim)
			if newPvc.ResourceVersion == oldPvc.ResourceVersion {
				return
			}
			controller.handlePVC(new)
		},
		DeleteFunc: controller.handlePVC,
	})

	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handlePV,
		UpdateFunc: func(old, new interface{}) {
			newPv := new.(*corev1.PersistentVolume)
			oldPv := old.(*corev1.PersistentVolume)
			if newPv.ResourceVersion == oldPv.ResourceVersion {
				return
			}
			controller.handlePV(new)
		},
		DeleteFunc: controller.handlePV,
	})

	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handlePod,
		UpdateFunc: func(old, new interface{}) {
			newPod := new.(*corev1.Pod)
			oldPod := old.(*corev1.Pod)
			if newPod.ResourceVersion == oldPod.ResourceVersion {
				return
			}
			controller.handlePod(new)
		},
		DeleteFunc: controller.handlePod,
	})

	helloInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleHello,
		UpdateFunc: func(old, new interface{}) {
			newHello := new.(*hellov1.Hello)
			oldHello := old.(*hellov1.Hello)
			if newHello.ResourceVersion == oldHello.ResourceVersion {
				return
			}
			controller.handleHello(new)
		},
		DeleteFunc: controller.handleHello,
	})

	return controller
}

func translateObject(obj interface{}) metav1.Object {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return nil
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return nil
		}
	}
	return object
}

func (c *controller) handleMapped(obj interface{}, objType string) {
	object := translateObject(obj)
	if nil == object {
		return
	}
	var key string
	if 0 == len(object.GetNamespace()) {
		key = objType + "/" + object.GetName()
	} else {
		key = objType + "/" + object.GetNamespace() + "/" + object.GetName()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.notifyMap[key]; ok {
		for k := range s.set {
			c.workqueue.Add(k)
		}
	}
}

func (c *controller) handlePVC(obj interface{}) {
	c.handleMapped(obj, "pvc")
	object := translateObject(obj)
	if nil == object {
		return
	}
	if workingNamespace != object.GetNamespace() {
		c.workqueue.Add("pvc/" + object.GetNamespace() + "/" + object.GetName())
	}
}

func (c *controller) handlePV(obj interface{}) {
	c.handleMapped(obj, "pv")
}

func (c *controller) handlePod(obj interface{}) {
	c.handleMapped(obj, "pod")
}

func (c *controller) handleHello(obj interface{}) {
	c.handleMapped(obj, "hello")
}

func (c *controller) Run(stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	ok := cache.WaitForCacheSync(stopCh, c.pvcSynced, c.pvSynced, c.podSynced, c.helloSynced)
	if !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	go wait.Until(c.runWorker, time.Second, stopCh)

	<-stopCh

	return nil
}

func (c *controller) runWorker() {
	processNextWorkItem := func(obj interface{}) error {
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		var err error
		parts := strings.Split(key, "/")
		switch parts[0] {
		case "pvc":
			if 3 != len(parts) {
				utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
				return nil
			}
			err = c.syncPvc(key, parts[1], parts[2])
		default:
			utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
			return nil
		}
		if nil != err {
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		c.workqueue.Forget(obj)
		return nil
	}

	for {
		obj, shutdown := c.workqueue.Get()
		if shutdown {
			return
		}
		err := processNextWorkItem(obj)
		if nil != err {
			utilruntime.HandleError(err)
		}
	}
}

func (c *controller) syncPvc(key, namespace, pvcName string) error {
	if workingNamespace == namespace {
		return nil
	}

	var err error

	var pvc *corev1.PersistentVolumeClaim
	pvc, err = c.pvcLister.PersistentVolumeClaims(namespace).Get(pvcName)
	if nil != err {
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("pvc '%s' in work queue no longer exists", key))
			return nil
		}
		return err
	}

	dataSource := pvc.Spec.DataSource
	if nil == dataSource {
		return nil
	}

	if hellov1.GroupName != *dataSource.APIGroup || "Hello" != dataSource.Kind || "" == dataSource.Name {
		return nil
	}

	c.callMeLater(key, "hello", namespace, dataSource.Name)
	var hello *hellov1.Hello
	hello, err = c.helloLister.Hellos(namespace).Get(dataSource.Name)
	if nil != err {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Use labels to find the stuff we create
	labelValue := string(pvc.UID)
	selectorSet := make(labels.Set)
	selectorSet[targetLabel] = labelValue

	// Look for the populator pod
	var podList []*corev1.Pod
	c.callMeLater(key, "pod", workingNamespace, populatePodName(pvc))
	podList, err = c.podLister.Pods(workingNamespace).List(labels.SelectorFromSet(selectorSet))
	if nil != err {
		return err
	}

	// Look for PVC'
	var pvcList []*corev1.PersistentVolumeClaim
	c.callMeLater(key, "pvc", workingNamespace, populatePvcName(pvc))
	pvcList, err = c.pvcLister.PersistentVolumeClaims(workingNamespace).List(labels.SelectorFromSet(selectorSet))
	if nil != err {
		return err
	}

	// Look for PV
	c.callMeLater(key, "pv", "", populatePvName(pvc))
	pv, err := c.pvLister.Get(populatePvName(pvc))
	if nil != err {
		if !errors.IsNotFound(err) {
			return err
		}
	}

	// *** Here is the first place we start to create/modify objects ***

	// If the PVC is unbound, we need to perform the restore
	if "" == pvc.Spec.VolumeName {

		// Ensure the PVC has a finalizer on it so we can clean up the stuff we create
		if finalizers, needUpdate := ensureFinalizer(pvc, pvcFinalizer, true); needUpdate {
			pvc = pvc.DeepCopy()
			pvc.Finalizers = finalizers
			_, err = c.kubeclientset.CoreV1().PersistentVolumeClaims(namespace).Update(pvc)
			if nil != err {
				return err
			}
		}

		// If the pod doesn't exist yet, create it
		if 0 == len(podList) {
			_, err = c.kubeclientset.CoreV1().Pods(workingNamespace).Create(makePopulatePod(labelValue, pvc, c.imageName, hello))
			if nil != err {
				return err
			}

			// If PVC' doesn't exist yet, create it
			if 0 == len(pvcList) {
				_, err = c.kubeclientset.CoreV1().PersistentVolumeClaims(workingNamespace).Create(makeRestorePvc(labelValue, pvc))
				if nil != err {
					return err
				}
			}

			// We'll get called again later when the pod exists
			return nil
		}

		pod := podList[0]
		if corev1.PodSucceeded != pod.Status.Phase {
			if corev1.PodFailed == pod.Status.Phase {
				// Delete failed pods so we can try again
				err = c.kubeclientset.CoreV1().Pods(workingNamespace).Delete(pod.Name, nil)
				if nil != err {
					return err
				}
			}
			// We'll get called again later when the pod succeeds
			return nil
		}

		// This would be bad
		if 0 == len(pvcList) {
			return fmt.Errorf("Failed to find PVC for retore pod")
		}
		pvcPrime := pvcList[0]

		// Get PV'
		var pvPrime *corev1.PersistentVolume
		c.callMeLater(key, "pv", "", pvcPrime.Spec.VolumeName)
		pvPrime, err = c.kubeclientset.CoreV1().PersistentVolumes().Get(pvcPrime.Spec.VolumeName, metav1.GetOptions{})
		if nil != err {
			if errors.IsNotFound(err) {
				// We'll get called again later when the PV exists
				return nil
			}
			return err
		}

		// Clone PV' to PV
		if nil == pv {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        populatePvName(pvc),
					Annotations: pvPrime.Annotations,
				},
			}
			if nil == pv.Annotations {
				pv.Annotations = make(map[string]string)
			}
			pv.Annotations[populatedFromAnno] = hello.Namespace + "/" + hello.Name
			pvPrime.Spec.DeepCopyInto(&pv.Spec)
			pv.Spec.ClaimRef = &corev1.ObjectReference{
				APIVersion:      "v1",
				Kind:            "PersistentVolumeClaim",
				Namespace:       pvc.Namespace,
				Name:            pvc.Name,
				UID:             pvc.UID,
				ResourceVersion: pvc.ResourceVersion,
			}
			_, err = c.kubeclientset.CoreV1().PersistentVolumes().Create(pv)
			if nil != err {
				return err
			}
		}

		// Update PV'
		if pvPrime.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
			pvPrime = pvPrime.DeepCopy()
			if nil == pvPrime.Labels {
				pvPrime.Labels = make(map[string]string)
			}
			pvPrime.Labels[targetLabel] = labelValue
			pvPrime.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
			_, err = c.kubeclientset.CoreV1().PersistentVolumes().Update(pvPrime)
			if nil != err {
				return err
			}
		}

		// Don't start cleaning up yet -- we need to bind controller to acknowledge
		// the switch
		return nil
	} else {
		// Look for PV'
		var pvList []*corev1.PersistentVolume
		pvList, err = c.pvLister.List(labels.SelectorFromSet(selectorSet))
		if nil != err {
			return err
		}

		if 0 < len(pvList) {
			pvPrime := pvList[0]
			err = c.kubeclientset.CoreV1().PersistentVolumes().Delete(pvPrime.Name, nil)
			if nil != err {
				return err
			}
		}
	}

	// *** At this point the volume population is done and we're just cleaning up ***

	// If the pod still exists, delete it
	if 0 < len(podList) {
		pod := podList[0]
		err = c.kubeclientset.CoreV1().Pods(workingNamespace).Delete(pod.Name, nil)
		if nil != err {
			return err
		}
	}

	// If PVC' still exists, delete it
	if 0 < len(pvcList) {
		pvcPrime := pvcList[0]
		err = c.kubeclientset.CoreV1().PersistentVolumeClaims(workingNamespace).Delete(pvcPrime.Name, nil)
		if nil != err {
			return err
		}
	}

	// Make sure the PVC finalizer is gone
	if finalizers, needUpdate := ensureFinalizer(pvc, pvcFinalizer, false); needUpdate {
		pvc = pvc.DeepCopy()
		pvc.Finalizers = finalizers
		_, err = c.kubeclientset.CoreV1().PersistentVolumeClaims(namespace).Update(pvc)
		if nil != err {
			return err
		}
	}

	c.cleanupNofications(key)

	return nil
}

func populatePvcName(targetPvc *corev1.PersistentVolumeClaim) string {
	return fmt.Sprintf("prime-%s", targetPvc.UID)
}

func populatePvName(targetPvc *corev1.PersistentVolumeClaim) string {
	return fmt.Sprintf("pvc-%s", targetPvc.UID)
}

func makeRestorePvc(labelValue string, targetPvc *corev1.PersistentVolumeClaim) *corev1.PersistentVolumeClaim {
	pvcPrime := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      populatePvcName(targetPvc),
			Namespace: workingNamespace,
			Labels: map[string]string{
				targetLabel: labelValue,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        targetPvc.Spec.Resources,
			StorageClassName: targetPvc.Spec.StorageClassName,
			VolumeMode:       targetPvc.Spec.VolumeMode,
		},
	}
	return pvcPrime
}

func populatePodName(targetPvc *corev1.PersistentVolumeClaim) string {
	return fmt.Sprintf("populate-%s", targetPvc.UID)
}

func makePopulatePod(labelValue string, targetPvc *corev1.PersistentVolumeClaim, imageName string, hello *hellov1.Hello) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      populatePodName(targetPvc),
			Namespace: workingNamespace,
			Labels: map[string]string{
				targetLabel: labelValue,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "populate",
					Image: imageName,
					Args: []string{
						"--mode=populate",
						"--file-name=/mnt/" + hello.Spec.FileName,
						"--file-contents=" + hello.Spec.FileContents,
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "target",
							MountPath: "/mnt",
						},
					},
					ImagePullPolicy: corev1.PullIfNotPresent,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{
				{
					Name: "target",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: populatePvcName(targetPvc),
						},
					},
				},
			},
		},
	}
	return pod
}

func ensureFinalizer(obj metav1.Object, finalizer string, want bool) ([]string, bool) {
	finalizers := obj.GetFinalizers()
	found := false
	foundIdx := -1
	for i, v := range finalizers {
		if finalizer == v {
			found = true
			foundIdx = i
			break
		}
	}
	if found == want {
		return []string{}, false
	}
	if want {
		return append(finalizers, finalizer), true
	} else {
		return append(finalizers[0:foundIdx], finalizers[foundIdx+1:]...), true
	}
}
