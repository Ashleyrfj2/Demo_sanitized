package routing

// ClaimFeasibleForRole reports whether the active/manual policy allows the
// requested role to validate a claim. M5B uses this only as a fail-closed
// feasibility guard for a human override; it does not require the replacement
// claim to outrank the router's original recommendation.
func ClaimFeasibleForRole(claimID, requestedRole string, policy Policy) bool {
	return roleAllowed(policyForClaim(policy, claimID), requestedRole)
}
