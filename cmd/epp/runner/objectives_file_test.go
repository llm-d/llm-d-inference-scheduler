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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	apixv1 "github.com/llm-d/llm-d-router/apix/v1"
)

type objectiveStore map[string]int32

func (s objectiveStore) ObjectiveSet(o *apixv1.InferenceObjective) { s[o.Name] = *o.Spec.Priority }

func writeObjectivesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "objectives.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoadObjectivesFile(t *testing.T) {
	path := writeObjectivesFile(t, `apiVersion: llm-d.ai/v1
kind: InferenceObjective
metadata:
  name: interactive
spec:
  priority: 10
---
apiVersion: llm-d.ai/v1
kind: InferenceObjective
metadata:
  name: batch
spec:
  priority: -1
---
apiVersion: llm-d.ai/v1
kind: InferenceObjective
metadata:
  name: unset
`)
	store := objectiveStore{}
	priorities, err := loadObjectivesFile(path, store)
	require.NoError(t, err)
	require.Equal(t, objectiveStore{"interactive": 10, "batch": -1, "unset": 0}, store)
	require.Equal(t, map[int]struct{}{10: {}, -1: {}, 0: {}}, priorities)

	priorities, err = loadObjectivesFile("", store)
	require.NoError(t, err)
	require.Empty(t, priorities)
}

func TestLoadObjectivesFileErrors(t *testing.T) {
	for name, content := range map[string]string{
		"wrong kind":       "apiVersion: llm-d.ai/v1\nkind: InferencePool\nmetadata:\n  name: p\n",
		"wrong version":    "apiVersion: llm-d.ai/v1alpha2\nkind: InferenceObjective\nmetadata:\n  name: o\n",
		"missing name":     "apiVersion: llm-d.ai/v1\nkind: InferenceObjective\nspec:\n  priority: 1\n",
		"invalid priority": "apiVersion: llm-d.ai/v1\nkind: InferenceObjective\nmetadata:\n  name: o\nspec:\n  priority: high\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadObjectivesFile(writeObjectivesFile(t, content), objectiveStore{})
			require.ErrorContains(t, err, "document 1")
		})
	}
	_, err := loadObjectivesFile(filepath.Join(t.TempDir(), "missing.yaml"), objectiveStore{})
	require.Error(t, err)
}
