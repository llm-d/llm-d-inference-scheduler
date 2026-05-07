/*
Copyright 2026 The Kubernetes Authors.

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

package epp

import (
	"testing"
)

// TestGenerateTraffic pins generateTraffic's outcome for a set of exec responses.
// Fixtures mirror real curl output: headers from -i plus the HTTP_STATUS trailer from -w.
// The HTTP/2 case guards against the reason phrase being dropped on an HTTP/2 connection.
func TestGenerateTraffic(t *testing.T) {
	cases := []struct {
		name           string
		response       string
		expectedStatus string
		wantErr        bool
	}{
		{"non-200 fails", "HTTP/1.1 503 Service Unavailable\r\n\r\nHTTP_STATUS=503\n", statusOK, true},
		{"200 succeeds", "HTTP/1.1 200 OK\r\n\r\nHTTP_STATUS=200\n", statusOK, false},
		// HTTP/2 drops the reason phrase ("HTTP/2 200" vs "HTTP/1.1 200 OK"); the trailer handles it.
		{"HTTP/2 200 succeeds", "HTTP/2 200\r\n\r\nHTTP_STATUS=200\n", statusOK, false},
		// empty expectedStatus accepts any exec-success (used when generating intentional errors)
		{`"" accepts 503`, "HTTP/1.1 503 Service Unavailable\r\n\r\nHTTP_STATUS=503\n", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execFn := func(_ []string) (string, error) { return tc.response, nil }
			semaphore := make(chan struct{}, 1)
			err := generateTraffic([]string{"curl"}, 1, semaphore, execFn, 0, tc.expectedStatus)
			if (err != nil) != tc.wantErr {
				t.Fatalf("generateTraffic: wantErr=%v, gotErr=%v", tc.wantErr, err)
			}
		})
	}
}
