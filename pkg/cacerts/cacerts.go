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

// Package cacerts parses the PEM CA bundle a Knative sink address carries
// (Addressable.CACerts, passed to the adapter as K_CA_CERTS). The controller
// uses it to reject a bad bundle with a status reason, and the adapter uses
// it to build the sink client's root pool.
package cacerts

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Parse decodes every PEM block in bundle. It fails when the bundle holds no
// CERTIFICATE block, when any block is not a CERTIFICATE, when a certificate
// does not parse, or when text other than whitespace sits outside the
// blocks. x509.CertPool.AppendCertsFromPEM skips bad blocks silently, which
// would turn a typo into a TLS failure much later; this is strict instead.
func Parse(bundle string) ([]*x509.Certificate, error) {
	rest := []byte(bundle)
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("CA bundle block %d is %q, want CERTIFICATE", len(certs)+1, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CA bundle certificate %d: %w", len(certs)+1, err)
		}
		certs = append(certs, cert)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("CA bundle has data that is not a PEM block")
	}
	if len(certs) == 0 {
		return nil, errors.New("CA bundle has no PEM CERTIFICATE block")
	}
	return certs, nil
}

// Pool returns the system root pool with the certificates of bundle added.
// The system roots stay in, matching Knative's semantics that the sink's
// CAs are added to the trusted set, not a replacement for it.
func Pool(bundle string) (*x509.CertPool, error) {
	certs, err := Parse(bundle)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}
