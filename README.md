
# Hello World Populator Prototype

This project demostrates an approach to populating Kubernetes volumes using a CRD
`DataSource`. For this example, the CRD and population mechanism are extremely
simple.

## How To

First you need to install golang 1.13 and docker, and clone this repo.

Build the image from code:

`make all`

Make sure you have a repo you can push to, and set the variable
 
`YOUR_REPO=...`

Push the image to your repo:

```
docker tag quay.io/k8scsi/hello-populator:canary ${YOUR_REPO}/hello-populator:canary
docker push ${YOUR_REPO}/hello-populator:canary
```

Install the CRD:

`kubectl apply -f config/crd/hello.k8s.io_hellos.yaml`

Install the controller:

`sed s_quay.io/k8scsi_${YOUR_REPO}_g < deploy/kubernetes.yaml | kubectl apply -f -`

Create a CR:

```
kubectl create -f - << EOF
apiVersion: hello.k8s.io/v1alpha1
kind: Hello
metadata:
  name: hello1
spec:
  fileName: test.txt
  fileContents: Hello, world!
EOF
```

Create a PVC:

```
kubectl create -f - << EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc1
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  dataSource:
    apiGroup: hello.k8s.io
    kind: Hello
    name: hello1
EOF
```

Create a pod:

```
kubectl create -f - << EOF
apiVersion: v1
kind: Pod
metadata:
  name: pod1
spec:
  containers:
    - name: test
      image: busybox:1.31.1
      command:
        - sleep
        - "3600"
      volumeMounts:
      - mountPath: /mnt
        name: persist
  volumes:
    - name: persist
      persistentVolumeClaim:
        claimName: pvc1
EOF
```

Wait for the pod to be ready:

`kubectl wait --for=condition=Ready --timeout=120s pod/pod1`

Print the contents of the pre-populated file inside the PVC:

`kubectl exec -i pod1 -- cat /mnt/test.txt`
