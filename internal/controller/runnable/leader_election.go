/**
 * This file is adapted from Gateway API Inference Extension
 * Original source: https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/internal/runnable/leader_election.go
 * Licensed under the Apache License, Version 2.0
 */

package runnable

import (
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// LeaderElection enables or disabled leader election for the provided manager.Runnable.
func LeaderElection(runnable manager.Runnable, needsLeaderElection bool) manager.Runnable {
	return &leaderElection{
		Runnable:            runnable,
		needsLeaderElection: needsLeaderElection,
	}
}

// RequireLeaderElection enables leader election for the provided manager.Runnable.
func RequireLeaderElection(runnable manager.Runnable) manager.Runnable {
	return LeaderElection(runnable, true)
}

// NoLeaderElection disabled leader election for the provided manager.Runnable.
func NoLeaderElection(runnable manager.Runnable) manager.Runnable {
	return LeaderElection(runnable, false)
}

// leaderElection is a wrapped manager.Runnable with configuration for enabling
// or disabling leader election.
type leaderElection struct {
	manager.Runnable
	needsLeaderElection bool
}

// NeedLeaderElection indicates whether or not leader election is enabled.
func (r *leaderElection) NeedLeaderElection() bool {
	return r.needsLeaderElection
}
