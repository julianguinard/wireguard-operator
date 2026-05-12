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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// KubernetesNameMaxLength is the maximum length for Kubernetes resource names
	KubernetesNameMaxLength = 63
	// HashLength is the length of the hash used when truncating names
	HashLength = 8
)

// SanitizeName ensures a Kubernetes resource name is valid and within the 63-character limit.
// If the name exceeds the limit, it truncates the base name and appends a hash to ensure uniqueness.
// The suffix is preserved as much as possible to maintain readability.
func SanitizeName(baseName, suffix string) string {
	fullName := baseName + suffix

	if len(fullName) <= KubernetesNameMaxLength {
		return fullName
	}

	// Need to truncate: reserve space for suffix and hash
	// Format: <truncated-base>-<hash><suffix>
	// We want to preserve the suffix as much as possible

	// Calculate available space for base name
	// Structure: <base>-<hash><suffix>
	// The hyphen before hash is included in the hash section
	suffixLen := len(suffix)

	// If suffix alone is too long, we need to truncate it too
	if suffixLen >= KubernetesNameMaxLength-HashLength-1 {
		// Even with minimal base and hash, suffix is too long
		// Truncate suffix and use minimal base
		maxSuffixLen := KubernetesNameMaxLength - HashLength - 1
		if maxSuffixLen < 0 {
			maxSuffixLen = 0
		}
		truncatedSuffix := suffix[:maxSuffixLen]
		hash := generateHash(baseName + suffix)
		return fmt.Sprintf("%s-%s%s", baseName[:1], hash, truncatedSuffix)
	}

	// Calculate max length for base name
	// Total = base + "-" + hash + suffix
	// 63 = base + 1 + 8 + suffixLen
	// base = 63 - 1 - 8 - suffixLen
	maxBaseLen := KubernetesNameMaxLength - 1 - HashLength - suffixLen

	if maxBaseLen < 1 {
		maxBaseLen = 1
	}

	// Truncate base name if needed
	truncatedBase := baseName
	if len(baseName) > maxBaseLen {
		truncatedBase = baseName[:maxBaseLen]
	}

	// Generate hash from original full name to ensure uniqueness
	hash := generateHash(baseName + suffix)

	return fmt.Sprintf("%s-%s%s", truncatedBase, hash, suffix)
}

// generateHash creates a short hash from a string
func generateHash(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])[:HashLength]
}

// SanitizeNameWithSeparator is like SanitizeName but allows customizing the separator
func SanitizeNameWithSeparator(baseName, separator, suffix string) string {
	fullName := baseName + separator + suffix

	if len(fullName) <= KubernetesNameMaxLength {
		return fullName
	}

	suffixLen := len(suffix)
	separatorLen := len(separator)

	// If suffix + separator is too long, truncate suffix
	if suffixLen+separatorLen >= KubernetesNameMaxLength-HashLength-1 {
		maxSuffixLen := KubernetesNameMaxLength - HashLength - 1
		if maxSuffixLen < 0 {
			maxSuffixLen = 0
		}
		truncatedSuffix := suffix[:maxSuffixLen]
		hash := generateHash(baseName + separator + suffix)
		return fmt.Sprintf("%s-%s%s", baseName[:1], hash, truncatedSuffix)
	}

	maxBaseLen := KubernetesNameMaxLength - separatorLen - HashLength - suffixLen
	if maxBaseLen < 1 {
		maxBaseLen = 1
	}

	truncatedBase := baseName
	if len(baseName) > maxBaseLen {
		truncatedBase = baseName[:maxBaseLen]
	}

	hash := generateHash(baseName + separator + suffix)

	return fmt.Sprintf("%s%s%s%s", truncatedBase, separator, hash, suffix)
}

// EnsureDNSSubdomain validates and sanitizes a name to be a valid DNS subdomain
// Kubernetes names must be valid DNS subdomains: lowercase alphanumeric, '-', or '.'
// Must start and end with alphanumeric character
func EnsureDNSSubdomain(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Replace invalid characters with '-'
	var result strings.Builder
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			// Kubernetes doesn't allow consecutive hyphens in some contexts
			if r == '-' && i > 0 && name[i-1] == '-' {
				continue
			}
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}

	name = result.String()

	// Ensure starts with alphanumeric
	if len(name) > 0 && !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= '0' && name[0] <= '9')) {
		name = "a" + name
	}

	// Ensure ends with alphanumeric
	if len(name) > 0 && !((name[len(name)-1] >= 'a' && name[len(name)-1] <= 'z') || (name[len(name)-1] >= '0' && name[len(name)-1] <= '9')) {
		name = name + "a"
	}

	// Truncate to max length if needed
	if len(name) > KubernetesNameMaxLength {
		// Use hash to ensure uniqueness
		hash := generateHash(name)
		maxLen := KubernetesNameMaxLength - HashLength - 1
		if maxLen < 1 {
			maxLen = 1
		}
		name = fmt.Sprintf("%s-%s", name[:maxLen], hash)
	}

	return name
}
