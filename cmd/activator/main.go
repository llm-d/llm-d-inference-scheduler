/*
Copyright 2025 The Kubernetes Authors.

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

// Package main implements the main entry point for the activator component.
package main

import (
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-inference-scheduler/cmd/activator/runner"
)

func main() {
	// For adding out-of-tree plugins to the plugins registry, use the following:
	// plugins.Register(my-out-of-tree-plugin-name, my-out-of-tree-plugin-factory-function)

	if err := runner.Run(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
