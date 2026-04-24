//go:build conformance
// +build conformance

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

package conformance

import (
	"crypto/rand"
	"encoding/hex"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// intFromInt wraps intstr.FromInt without the 32-bit overflow lint noise.
// Conformance port numbers are always well within int32 range.
func intFromInt(i int) intstr.IntOrString {
	return intstr.FromInt(i)
}

// randString returns a lowercase hex string of n bytes (2n chars). Used for
// namespace uniqueness across parallel/repeat runs.
func randString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a non-random constant; collision probability is fine
		// for test namespaces since t.Cleanup scrubs them anyway.
		return "deadbeefdeadbeef"[:2*n]
	}
	return hex.EncodeToString(b)
}
