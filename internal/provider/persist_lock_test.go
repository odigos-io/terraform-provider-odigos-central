// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockPersistSerializesSameKey(t *testing.T) {
	c := &cachedOdigosClient{}
	var inFlight, maxInFlight atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := c.lockPersist(sourcePersistKey("c", "ns", "Deployment", "ads"))
			defer unlock()
			n := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("maxInFlight in-flight = %d, want 1", got)
	}
}

func TestLockPersistAllowsDifferentKeys(t *testing.T) {
	c := &cachedOdigosClient{}
	started := make(chan struct{})
	released := make(chan struct{})

	unlockA := c.lockPersist(sourcePersistKey("c", "ns", "Deployment", "ads"))
	go func() {
		close(started)
		unlockB := c.lockPersist(sourcePersistKey("c", "ns", "Deployment", "other"))
		unlockB()
		close(released)
	}()
	<-started
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("lock on a different workload blocked")
	}
	unlockA()
}

func TestAnnotateDuplicateSourceError(t *testing.T) {
	err := errors.New(`graphql "PersistSources": failed to sync workloads: expected to get 1 source "frontend", got 2`)
	got := annotateDuplicateSourceError(err)
	if !strings.Contains(got, "Delete extra Source CRDs") {
		t.Errorf("annotated error missing cleanup hint: %s", got)
	}
}

func TestSourceNotYetCreated(t *testing.T) {
	if sourceNotYetCreated(nil) {
		t.Error("nil error is not a missing source")
	}
	if sourceNotYetCreated(errors.New("graphql \"GetSource\": connection refused")) {
		t.Error("transport failure must still fail Create")
	}
	err := errors.New(`graphql "GetSource": failed to get InstrumentationConfig: instrumentationconfigs.odigos.io "deployment-pricing" not found`)
	if !sourceNotYetCreated(err) {
		t.Errorf("missing InstrumentationConfig should be treated as not yet a source: %v", err)
	}
}

func TestExistingMembershipStream(t *testing.T) {
	if got := existingMembershipStream([]string{"default"}, "defaultTwoStream"); got != "default" {
		t.Errorf("got %q, want default", got)
	}
	if got := existingMembershipStream([]string{"default", "defaultTwoStream"}, "defaultTwoStream"); got != "default" {
		t.Errorf("got %q, want default when the planned stream is also listed", got)
	}
	if got := existingMembershipStream([]string{"defaultTwoStream"}, "defaultTwoStream"); got != "defaultTwoStream" {
		t.Errorf("got %q, want the only listed stream", got)
	}
}
