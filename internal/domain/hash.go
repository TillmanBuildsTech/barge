package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Labels barge puts on every container it creates. They are how barge
// recognises what it owns: it never modifies or adopts a container that lacks
// them.
const (
	// LabelManagedBy marks a container as owned by barge.
	LabelManagedBy = "managed-by"
	// ManagedByValue is the value of LabelManagedBy.
	ManagedByValue = "barge"
	// LabelPrefix is reserved for barge's own labels.
	LabelPrefix = "barge."
	// LabelApp records which configured app a container belongs to.
	LabelApp = LabelPrefix + "app"
	// LabelSpecHash records Hash() of the spec the container was made from.
	LabelSpecHash = LabelPrefix + "spec-hash"
)

// ReservedLabel reports whether users may not set the label themselves.
func ReservedLabel(key string) bool {
	return key == LabelManagedBy || strings.HasPrefix(key, LabelPrefix)
}

// canonical is the hashed form: field order is fixed, empty collections are
// omitted (so nil and empty are equal) and encoding/json sorts map keys.
type canonical struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Ports   []PortMapping     `json:"ports,omitempty"`
	Volumes []VolumeMount     `json:"volumes,omitempty"`
	Restart RestartPolicy     `json:"restart,omitempty"`
}

// Hash returns a stable fingerprint (32 hex chars) of everything in the spec
// that affects the container. Barge stores it as LabelSpecHash and redeploys
// when it differs. Collection order is significant for slices, not for maps.
//
// Note: env values are part of the hash, and the hash is readable by anyone
// who can inspect the container. Once secret references exist, hash the
// reference, not the resolved value.
func (s ContainerSpec) Hash() string {
	// A direct conversion: adding a field to ContainerSpec without adding it
	// to canonical (and so to the hash) is a compile error.
	b, err := json.Marshal(canonical(s))
	if err != nil {
		panic("domain: hashing a ContainerSpec cannot fail: " + err.Error()) // only strings/ints/maps of them
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
