// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos-central-client/central"
)

// TestProxyByNameCachedUnsupportedClusterWarns covers the negative-cache branch.
// ProxyByName stores a typed nil *central.ProxyClient to remember that a cluster
// is unsupported. Because a typed nil is not == nil once held in an interface,
// a naive `v == nil` check silently skips the warning, leaving the practitioner
// with no explanation for why the cluster was ignored.
func TestProxyByNameCachedUnsupportedClusterWarns(t *testing.T) {
	// client is intentionally nil: a cache hit must return before any call to it.
	c := &cachedOdigosClient{}
	c.proxies.Store("unsupported-cluster", (*central.ProxyClient)(nil))

	proxy, diags := c.ProxyByName(context.Background(), "unsupported-cluster")

	if proxy != nil {
		t.Error("expected a nil proxy for a cluster cached as unsupported")
	}
	if diags.HasError() {
		t.Errorf("an unsupported cluster is a soft failure, got error diagnostics: %v", diags)
	}
	if got := diags.WarningsCount(); got != 1 {
		t.Errorf("expected exactly 1 warning explaining the cluster was skipped, got %d: %v", got, diags)
	}
}

// TestProxyByNameCacheHitReturnsProxy checks the positive path: a cached proxy is
// returned as-is with no diagnostics and without re-resolving.
func TestProxyByNameCacheHitReturnsProxy(t *testing.T) {
	cached := &central.ProxyClient{}

	c := &cachedOdigosClient{}
	c.proxies.Store("supported-cluster", cached)

	proxy, diags := c.ProxyByName(context.Background(), "supported-cluster")

	if proxy != cached {
		t.Errorf("expected the cached proxy to be returned, got %v", proxy)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics on a cache hit, got: %v", diags)
	}
}
