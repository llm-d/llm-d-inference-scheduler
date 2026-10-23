/*
Copyright 2026 The llm-d Authors.

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

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"

	apixv1 "github.com/llm-d/llm-d-router/apix/v1"
)

type objectiveSetter interface {
	ObjectiveSet(*apixv1.InferenceObjective)
}

// loadObjectivesFile stores the InferenceObjectives in path in ds and returns their
// priorities. File-discovery mode has no reconciler to do this. Pool references are
// not checked: a file-discovery EPP serves a single pool. An empty path is a no-op.
func loadObjectivesFile(path string, ds objectiveSetter) (map[int]struct{}, error) {
	priorities := map[int]struct{}{}
	if path == "" {
		return priorities, nil
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for doc := 1; ; doc++ {
		o := &apixv1.InferenceObjective{}
		if err := decoder.Decode(o); errors.Is(err, io.EOF) {
			return priorities, nil
		} else if err != nil {
			return nil, fmt.Errorf("%s, document %d: %w", path, doc, err)
		}
		if o.APIVersion != apixv1.GroupVersion.String() || o.Kind != "InferenceObjective" || o.Name == "" {
			return nil, fmt.Errorf("%s, document %d: want a named %s InferenceObjective", path, doc, apixv1.GroupVersion.String())
		}
		if o.Spec.Priority == nil {
			// The API defines an unset priority as 0.
			o.Spec.Priority = ptr.To(int32(0))
		}
		ds.ObjectiveSet(o)
		priorities[int(*o.Spec.Priority)] = struct{}{}
	}
}
