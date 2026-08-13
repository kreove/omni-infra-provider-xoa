// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

// A stand-in for a real service account key: a long single-line base64 blob.
func sampleKey() string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("omni-service-account-payload", 40)))
}

// Go's base64 decoder ignores \r and \n, so line wrapping alone never breaks a
// key. Spaces and tabs are not ignored and are the characters that actually
// produce "illegal base64 data at input byte N". This test pins that
// distinction down so the error message stays accurate.
func TestNewlinesAreToleratedByTheDecoder(t *testing.T) {
	t.Parallel()

	clean := sampleKey()

	for name, wrapped := range map[string]string{
		"trailing newline":  clean + "\n",
		"trailing CRLF":     clean + "\r\n",
		"wrapped mid-value": clean[:600] + "\n" + clean[600:],
		"CRLF wrapped":      clean[:600] + "\r\n" + clean[600:],
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := base64.StdEncoding.DecodeString(wrapped); err != nil {
				t.Fatalf("newlines should be ignored by the decoder, but %s failed: %v", name, err)
			}
		})
	}
}

func TestStripWhitespaceRepairsFatalCorruption(t *testing.T) {
	t.Parallel()

	clean := sampleKey()

	// Every case here is genuinely fatal to base64.StdEncoding.
	cases := map[string]string{
		"leading space":         " " + clean,
		"trailing space":        clean + " ",
		"space mid-value":       clean[:600] + " " + clean[600:],
		"tab mid-value":         clean[:600] + "\t" + clean[600:],
		"indented continuation": clean[:600] + "\n    " + clean[600:],
	}

	for name, corrupted := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := base64.StdEncoding.DecodeString(corrupted); err == nil {
				t.Fatalf("expected %s to be rejected by the strict decoder", name)
			}

			repaired := stripWhitespace(corrupted)
			if repaired != clean {
				t.Fatalf("stripWhitespace did not restore the original key for %s", name)
			}

			if err := validateServiceAccountKey(repaired); err != nil {
				t.Fatalf("repaired key should validate, got: %v", err)
			}
		})
	}
}

// The failure actually hit in the field: a duplicated trailing '=' on a key
// pasted into a .env file. Whitespace stripping cannot rescue this, so the
// error must explain it clearly.
func TestExtraPaddingIsReportedClearly(t *testing.T) {
	t.Parallel()

	clean := sampleKey()
	if !strings.HasSuffix(clean, "=") {
		t.Skipf("sample key does not end in padding (%q), cannot construct the case", clean[len(clean)-4:])
	}

	corrupted := clean + "="

	err := validateServiceAccountKey(corrupted)
	if err == nil {
		t.Fatal("an extra '=' must be rejected")
	}

	msg := err.Error()
	for _, want := range []string{"base64 padding", "removing the trailing '='", "multiple of 4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %v", want, msg)
		}
	}

	// And the clean key must still be accepted.
	if err := validateServiceAccountKey(clean); err != nil {
		t.Fatalf("clean key should validate, got: %v", err)
	}
}

func TestValidateServiceAccountKeyRejectsGenuinelyBadInput(t *testing.T) {
	t.Parallel()

	// '!' is not in the base64 alphabet and is not whitespace, so stripping
	// cannot rescue it; this must still be reported.
	if err := validateServiceAccountKey("not!valid!base64!"); err == nil {
		t.Fatal("expected an error for a non-base64 key")
	}

	// An unset key is allowed; the provider may be configured another way.
	if err := validateServiceAccountKey(""); err != nil {
		t.Fatalf("empty key should be accepted, got: %v", err)
	}
}

func TestValidateServiceAccountKeyErrorIsActionable(t *testing.T) {
	t.Parallel()

	err := validateServiceAccountKey("not!valid!base64!")
	if err == nil {
		t.Fatal("expected an error")
	}

	// The message should identify the offending character and the remedy,
	// rather than just repeating a byte offset.
	for _, want := range []string{"renewkey", "single-line", "not part of the base64 alphabet", `"!"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message should mention %q, got: %v", want, err)
		}
	}
}
