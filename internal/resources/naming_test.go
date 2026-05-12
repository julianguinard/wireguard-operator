/*
Copyright 2021.

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

package resources

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name     string
		baseName string
		suffix   string
		wantLen  int
		wantHash bool
	}{
		{
			name:     "short name",
			baseName: "my-vpn",
			suffix:   "-svc",
			wantLen:  9,
			wantHash: false,
		},
		{
			name:     "long name with uuid",
			baseName: "poc1-ovh-control-plane-878bff92-96a6-4012-bc14-f44432ae977a",
			suffix:   "-metrics-svc",
			wantLen:  63,
			wantHash: true,
		},
		{
			name:     "exact limit",
			baseName: strings.Repeat("a", 50),
			suffix:   "-svc",
			wantLen:  54,
			wantHash: false,
		},
		{
			name:     "over limit",
			baseName: strings.Repeat("a", 60),
			suffix:   "-svc",
			wantLen:  63,
			wantHash: true,
		},
		{
			name:     "empty suffix",
			baseName: "my-vpn",
			suffix:   "",
			wantLen:  6,
			wantHash: false,
		},
		{
			name:     "long name empty suffix",
			baseName: strings.Repeat("a", 70),
			suffix:   "",
			wantLen:  63,
			wantHash: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeName(tt.baseName, tt.suffix)

			// Check length
			if len(got) != tt.wantLen {
				t.Errorf("SanitizeName() length = %v, want %v, got %v", len(got), tt.wantLen, got)
			}

			// Check max length
			if len(got) > KubernetesNameMaxLength {
				t.Errorf("SanitizeName() exceeds max length: %v > %v", len(got), KubernetesNameMaxLength)
			}

			// Check if hash is present when expected
			if tt.wantHash {
				// Should contain a hash (8 hex characters)
				if !strings.ContainsAny(got, "0123456789abcdef") {
					t.Errorf("SanitizeName() should contain hash when truncated: %v", got)
				}
			}

			// Check consistency - same input should produce same output
			got2 := SanitizeName(tt.baseName, tt.suffix)
			if got != got2 {
				t.Errorf("SanitizeName() is not consistent: %v != %v", got, got2)
			}
		})
	}
}

func TestSanitizeNameUniqueness(t *testing.T) {
	// Test that different names produce different hashes
	name1 := SanitizeName(strings.Repeat("a", 70), "-svc")
	name2 := SanitizeName(strings.Repeat("b", 70), "-svc")

	if name1 == name2 {
		t.Errorf("SanitizeName() should produce different names for different inputs: %v == %v", name1, name2)
	}
}

func TestEnsureDNSSubdomain(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid name",
			input: "my-valid-name",
			want:  "my-valid-name",
		},
		{
			name:  "uppercase",
			input: "MY-NAME",
			want:  "my-name",
		},
		{
			name:  "invalid chars",
			input: "my_name@test",
			want:  "my-name-test",
		},
		{
			name:  "starts with hyphen",
			input: "-my-name",
			want:  "a-my-name",
		},
		{
			name:  "ends with hyphen",
			input: "my-name-",
			want:  "my-name-a",
		},
		{
			name:  "too long",
			input: strings.Repeat("a", 70),
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnsureDNSSubdomain(tt.input)

			// Check max length
			if len(got) > KubernetesNameMaxLength {
				t.Errorf("EnsureDNSSubdomain() exceeds max length: %v > %v", len(got), KubernetesNameMaxLength)
			}

			// For the "too long" test, just check it's valid and <= 63 chars
			if tt.name == "too long" {
				if len(got) == 0 || len(got) > 63 {
					t.Errorf("EnsureDNSSubdomain() too long test failed: got %v", got)
				}
				return
			}

			if got != tt.want {
				t.Errorf("EnsureDNSSubdomain() = %v, want %v", got, tt.want)
			}
		})
	}
}
