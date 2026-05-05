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

package datalayer

import (
	"fmt"
	"maps"

	"k8s.io/apimachinery/pkg/types"
)

// EndpointMetadata represents the relevant Kubernetes Pod state of an inference server.
type EndpointMetadata struct {
	NamespacedName types.NamespacedName
	PodName        string
	Address        string
	Port           string
	MetricsHost    string
	Labels         map[string]string
}

// String returns a string representation of the endpoint.
func (e *EndpointMetadata) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%+v", *e)
}

// Clone returns a full copy of the object.
func (e *EndpointMetadata) Clone() *EndpointMetadata {
	if e == nil {
		return nil
	}

	clonedLabels := make(map[string]string, len(e.Labels))
	maps.Copy(clonedLabels, e.Labels)
	return &EndpointMetadata{
		NamespacedName: types.NamespacedName{
			Name:      e.NamespacedName.Name,
			Namespace: e.NamespacedName.Namespace,
		},
		PodName:     e.PodName,
		Address:     e.Address,
		Port:        e.Port,
		MetricsHost: e.MetricsHost,
		Labels:      clonedLabels,
	}
}

// GetNamespacedName gets the namespace name of the Endpoint.
func (e *EndpointMetadata) GetNamespacedName() types.NamespacedName {
	return e.NamespacedName
}

// GetIPAddress returns the Endpoint's IP address.
func (e *EndpointMetadata) GetIPAddress() string {
	return e.Address
}

// GetPort returns the Endpoint's inference port.
func (e *EndpointMetadata) GetPort() string {
	return e.Port
}

// GetMetricsHost returns the Endpoint's metrics host (ip:port)
func (e *EndpointMetadata) GetMetricsHost() string {
	return e.MetricsHost
}
