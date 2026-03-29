// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "time"

// ConfigProvider provides read access to mtix configuration.
// The service layer reads configuration but never mutates it.
type ConfigProvider interface {
	// AutoClaim returns whether auto-claim is enabled per FR-11.2a.
	// When true and a node is created under a claimed parent, the child
	// is auto-claimed for the same agent in the same transaction.
	AutoClaim() bool

	// SoftDeleteRetention returns the retention period for soft-deleted nodes per FR-3.3.
	// Nodes whose deleted_at exceeds this duration are permanently removed.
	SoftDeleteRetention() time.Duration

	// AgentStaleThreshold returns the duration after which an agent is considered stale per FR-10.4a.
	AgentStaleThreshold() time.Duration

	// MaxRecommendedDepth returns the advisory maximum hierarchy depth per FR-1.1a.
	MaxRecommendedDepth() int

	// AgentStuckTimeout returns the duration after which a stuck agent's node
	// is auto-unclaimed per FR-10.3a. Zero means no auto-unclaim.
	AgentStuckTimeout() time.Duration

	// SessionTimeout returns the maximum session duration before auto-end per FR-10.5a.
	// Default: 4h.
	SessionTimeout() time.Duration
}

// StaticConfig is a simple ConfigProvider for testing and CLI defaults.
type StaticConfig struct {
	AutoClaimEnabled    bool
	RetentionDuration   time.Duration
	StaleThreshold      time.Duration
	RecommendedMaxDepth int
	StuckTimeout        time.Duration
	SessionTimeoutDur   time.Duration
}

// AutoClaim returns the configured auto-claim setting.
func (c *StaticConfig) AutoClaim() bool {
	return c.AutoClaimEnabled
}

// SoftDeleteRetention returns the configured retention period.
func (c *StaticConfig) SoftDeleteRetention() time.Duration {
	if c.RetentionDuration == 0 {
		return 30 * 24 * time.Hour // Default: 30 days.
	}
	return c.RetentionDuration
}

// AgentStaleThreshold returns the configured stale threshold.
func (c *StaticConfig) AgentStaleThreshold() time.Duration {
	if c.StaleThreshold == 0 {
		return 24 * time.Hour // Default: 24h.
	}
	return c.StaleThreshold
}

// MaxRecommendedDepth returns the configured max depth.
func (c *StaticConfig) MaxRecommendedDepth() int {
	if c.RecommendedMaxDepth == 0 {
		return 50
	}
	return c.RecommendedMaxDepth
}

// AgentStuckTimeout returns the configured stuck timeout.
func (c *StaticConfig) AgentStuckTimeout() time.Duration {
	return c.StuckTimeout
}

// SessionTimeout returns the configured session timeout.
func (c *StaticConfig) SessionTimeout() time.Duration {
	if c.SessionTimeoutDur == 0 {
		return 4 * time.Hour // Default: 4h per FR-10.5a.
	}
	return c.SessionTimeoutDur
}
