/*
Copyright 2026 The YipYap Authors

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

package cacerts

import (
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

func TestParse(t *testing.T) {
	ca := testCAPEM(t)
	cases := []struct {
		name    string
		bundle  string
		want    int
		wantErr string
	}{
		{name: "one cert", bundle: ca, want: 1},
		{name: "two certs with whitespace", bundle: "\n" + ca + "\n\n" + ca + "  \n", want: 2},
		{name: "empty", bundle: "", wantErr: "no PEM CERTIFICATE"},
		{name: "garbage", bundle: "not a cert", wantErr: "not a PEM block"},
		{name: "trailing garbage", bundle: ca + "oops", wantErr: "not a PEM block"},
		{name: "private key block", bundle: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n", wantErr: `"PRIVATE KEY"`},
		{name: "bad DER", bundle: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", wantErr: "certificate 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.bundle)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Parse() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("Parse() returned %d certs, want %d", len(got), tc.want)
			}
		})
	}
}

func TestPoolRejectsBadBundle(t *testing.T) {
	if _, err := Pool("junk"); err == nil {
		t.Fatal("Pool(junk) succeeded, want error")
	}
	if p, err := Pool(testCAPEM(t)); err != nil || p == nil {
		t.Fatalf("Pool(valid) = %v, %v", p, err)
	}
}
