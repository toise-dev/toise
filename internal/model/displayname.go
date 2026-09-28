package model

// displayNameKeys names, per entity type, the descriptive attributes that hold
// what a human calls the thing, in order of preference. A type absent from this
// map has no display name: its identity already reads as a name (a db instance
// id is "redis:6379@<host>"), or nothing observed on it is more legible than the
// identity itself.
//
// These keys are for RENDERING ONLY. A display name drifts — a host is renamed
// while its host.id does not — so nothing may key, join, or match on one. That
// is the whole reason identity lives in Identity and this lives apart from it.
var displayNameKeys = map[string][]string{
	TypeHost:             {"host.name"},
	TypeContainer:        {"container.name", "compose.service"},
	TypeServiceInstance:  {"service.name"},
	TypeNetworkDevice:    {"network.device.name", "device.name"},
	TypeNetworkInterface: {"interface.name"},
	TypeProcess:          {"process.name", "process.executable.name"},
	TypePod:              {"k8s.pod.name"},
	TypeComputeVM:        {"vm.name", "host.name"},
}

// DisplayName returns the human-readable name of an entity, or "" when it has
// none worth showing.
//
// It returns "" when the preferred key is itself an identifying attribute: the
// name is already in the identity, and repeating it would pad every label of
// that type with a duplicate.
//
// The point is scanning. An entity's identity is frequently a UUID or a 64-char
// digest, so a caller listing many entities — the compact verbosity that exists
// precisely to scan cheaply — used to see nothing it could recognize, even
// though host.name and container.name were present on every one of them. An
// answer that hides what it holds reads exactly like an answer that holds
// nothing (#378).
func DisplayName(e Entity) string {
	keys, ok := displayNameKeys[e.Type]
	if !ok {
		return ""
	}
	for _, key := range keys {
		if hasIdentityKey(e, key) {
			return ""
		}
		for _, kv := range e.Attributes {
			if kv.Key == key {
				if s := kv.Value.Display(); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func hasIdentityKey(e Entity, key string) bool {
	for _, kv := range e.Identity {
		if kv.Key == key {
			return true
		}
	}
	return false
}
