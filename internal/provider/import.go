// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// parseSlashID splits a terraform import ID on '/' and requires exactly n
// non-empty segments. Kubernetes names and Odigos stream names cannot contain
// '/', so this is unambiguous for the identifiers this provider uses.
func parseSlashID(id string, n int, format string) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	parts := strings.Split(id, "/")
	if len(parts) != n {
		diags.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected %s, got %q.", format, id),
		)
		return nil, diags
	}
	for _, part := range parts {
		if part == "" {
			diags.AddError(
				"Invalid Import ID",
				fmt.Sprintf("Expected %s, got %q (empty segment).", format, id),
			)
			return nil, diags
		}
	}
	return parts, diags
}
