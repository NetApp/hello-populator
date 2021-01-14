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
	"strings"

	hellov1 "github.com/kubernetes-csi/hello-populator/client/apis/hello/v1alpha1"
	populator_machinery "github.com/kubernetes-csi/hello-populator/pkg/populator-machinery"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	namespace  = "hello"
	prefix     = "hello.k8s.io"
	mountPath  = "/mnt"
	devicePath = "/dev/block"
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
	flag.StringVar(&mode, "mode", "", "Mode to run in (controller, populate)")
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
		gk := hellov1.SchemeGroupVersion.WithKind("Hello").GroupKind()
		gvr := hellov1.SchemeGroupVersion.WithResource("hellos")
		populator_machinery.RunController(masterURL, kubeconfig, imageName,
			namespace, prefix, gk, gvr, mountPath, devicePath, getPopulatorPodArgs)
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

func getPopulatorPodArgs(rawBlock bool, u *unstructured.Unstructured) ([]string, error) {
	var hello hellov1.Hello
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), &hello)
	if nil != err {
		return nil, err
	}
	args := []string{"--mode=populate"}
	if rawBlock {
		args = append(args, "--file-name="+devicePath)
	} else {
		args = append(args, "--file-name="+mountPath+"/"+hello.Spec.FileName)
	}
	args = append(args, "--file-contents="+hello.Spec.FileContents)
	return args, nil
}
