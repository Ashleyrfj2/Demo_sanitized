package httpapi

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"
)

// SourceIdentity is the server-owned identity attached to an evidence source.
// Event payloads may describe the same identity, but they cannot override it.
type SourceIdentity struct {
	TenantID      string
	ActorID       string
	SourceType    string
	SourceAdapter string
	Role          string
	Capabilities  map[string]bool
}

func (identity SourceIdentity) HasCapability(capability string) bool {
	return identity.Capabilities[capability]
}

type Authenticator interface {
	Authenticate(token string) (SourceIdentity, bool)
}

type StaticAuthenticator struct {
	entries []authEntry
}

type authEntry struct {
	token    string
	identity SourceIdentity
}

// NewStaticAuthenticator creates an in-memory bearer-token registry. Tokens
// remain private to the registry and are compared in constant time.
func NewStaticAuthenticator(entries map[string]SourceIdentity) *StaticAuthenticator {
	authenticator := &StaticAuthenticator{entries: make([]authEntry, 0, len(entries))}
	for token, identity := range entries {
		if len(identity.Capabilities) == 0 {
			identity.Capabilities = map[string]bool{"source": true}
		}
		authenticator.entries = append(authenticator.entries, authEntry{token: token, identity: identity})
	}
	return authenticator
}

func (authenticator *StaticAuthenticator) Authenticate(token string) (SourceIdentity, bool) {
	for _, entry := range authenticator.entries {
		if len(token) == len(entry.token) && subtle.ConstantTimeCompare([]byte(token), []byte(entry.token)) == 1 {
			return entry.identity, true
		}
	}
	return SourceIdentity{}, false
}

// LoadAuthenticator reads a semicolon-separated local registry from
// QA_SOURCE_IDENTITIES. Each entry is:
// token|tenant_id|actor_id|source_type|source_adapter|role[|capability,...]
func LoadAuthenticator() (*StaticAuthenticator, error) {
	raw := strings.TrimSpace(os.Getenv("QA_SOURCE_IDENTITIES"))
	if raw == "" {
		return nil, fmt.Errorf("QA_SOURCE_IDENTITIES is required")
	}
	entries := map[string]SourceIdentity{}
	for index, encoded := range strings.Split(raw, ";") {
		parts := strings.Split(encoded, "|")
		if len(parts) != 6 && len(parts) != 7 {
			return nil, fmt.Errorf("QA_SOURCE_IDENTITIES entry %d must contain six or seven fields", index+1)
		}
		for fieldIndex := range parts {
			parts[fieldIndex] = strings.TrimSpace(parts[fieldIndex])
			if parts[fieldIndex] == "" {
				return nil, fmt.Errorf("QA_SOURCE_IDENTITIES entry %d contains an empty field", index+1)
			}
		}
		if parts[3] != "human" && parts[3] != "agent" && parts[3] != "automation" {
			return nil, fmt.Errorf("QA_SOURCE_IDENTITIES entry %d has invalid source type", index+1)
		}
		if _, exists := entries[parts[0]]; exists {
			return nil, fmt.Errorf("QA_SOURCE_IDENTITIES contains a duplicate token")
		}
		capabilities := map[string]bool{"source": true}
		if len(parts) == 7 {
			capabilities = map[string]bool{}
			for _, capability := range strings.Split(parts[6], ",") {
				capability = strings.TrimSpace(capability)
				if capability != "source" && capability != "controller" {
					return nil, fmt.Errorf("QA_SOURCE_IDENTITIES entry %d has invalid capability", index+1)
				}
				capabilities[capability] = true
			}
		}
		entries[parts[0]] = SourceIdentity{TenantID: parts[1], ActorID: parts[2], SourceType: parts[3], SourceAdapter: parts[4], Role: parts[5], Capabilities: capabilities}
	}
	return NewStaticAuthenticator(entries), nil
}
