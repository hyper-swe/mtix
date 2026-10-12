// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// Grant matching keeps routing observations separate from operator confirmations.

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

const testBoardID = "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"

func TestGrantMatch_StrictOff_BoardIDOnly(t *testing.T) {
	grant := Grant{BoardID: testBoardID, Hub: HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}}
	for _, observed := range []HubIdentity{{}, grant.Hub, {Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "other"}} {
		assert.True(t, grant.Matches(testBoardID, observed, false))
	}
	assert.False(t, grant.Matches("", grant.Hub, false))
	assert.False(t, grant.Matches("4549495e-f331-4ac3-86af-decd9cbcbda6", grant.Hub, false))
	assert.False(t, (Grant{BoardID: "invalid"}).Matches("invalid", HubIdentity{}, false))
}

func TestGrantMatch_StrictOn_UnknownHubIdentity_NoMatch(t *testing.T) {
	hub := HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	grant := Grant{BoardID: testBoardID, Hub: hub}
	assert.True(t, grant.Matches(testBoardID, hub, true))
	for _, observed := range []HubIdentity{{}, {Kind: "postgres", ProjectPrefix: "TEST"}, {Kind: "postgres", ProjectPrefix: "OTHER", FirstEventHash: "first"}, {Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "other"}, {Kind: "relay", RelayID: "019b8e44-42b3-7000-8000-000000000001"}} {
		assert.False(t, grant.Matches(testBoardID, observed, true))
	}
	assert.False(t, (Grant{BoardID: testBoardID}).Matches(testBoardID, hub, true))
}
