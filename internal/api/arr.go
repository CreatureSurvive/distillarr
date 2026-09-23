package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"mediatrans/internal/config"
)

// arrInstanceOut is an ArrInstance as returned by the API: the key is
// never sent back, matching the Jellyfin key's masking convention.
type arrInstanceOut struct {
	config.ArrInstance
	APIKey    string `json:"api_key"`
	APIKeySet bool   `json:"api_key_set"`
}

func arrOut(inst config.ArrInstance) arrInstanceOut {
	keySet := inst.APIKey != ""
	inst.APIKey = ""
	return arrInstanceOut{ArrInstance: inst, APIKeySet: keySet}
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a display name into a lowercase, hyphenated id fragment.
// A stable id is needed (not the name itself) because webhook URLs and
// arr_items rows key on it, and the name can be edited later.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "instance"
	}
	return s
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mergeArrInstances resolves a PUT /config patch's arr_instances against
// the currently stored list. The patch is the complete desired list (an
// entry left out is removed, matching the rest of putConfig's
// object-replacement semantics for this field): each entry is matched by
// id, a blank api_key on an existing id keeps the stored key, and an
// entry with no id is new and gets a slug of its name (deduplicated
// against every id already in use, stored or freshly assigned).
func mergeArrInstances(stored []config.ArrInstance, patch json.RawMessage) ([]config.ArrInstance, error) {
	var incoming []config.ArrInstance
	if err := json.Unmarshal(patch, &incoming); err != nil {
		return nil, fmt.Errorf("arr_instances: %w", err)
	}
	byID := make(map[string]config.ArrInstance, len(stored))
	for _, s := range stored {
		byID[s.ID] = s
	}
	used := make(map[string]bool, len(incoming))
	out := make([]config.ArrInstance, 0, len(incoming))
	for _, inst := range incoming {
		switch {
		case inst.ID == "":
			base := slugify(inst.Name)
			id := base
			for used[id] || byID[id].ID != "" {
				id = base + "-" + randomSuffix()
			}
			inst.ID = id
		case strings.TrimSpace(inst.APIKey) == "":
			if old, ok := byID[inst.ID]; ok {
				inst.APIKey = old.APIKey
			}
		}
		used[inst.ID] = true
		out = append(out, inst)
	}
	return out, nil
}
