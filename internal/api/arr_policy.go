// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// ArrPolicyFor builds the recs.ArrPolicy hook (wired from main.go
// alongside recs.Genres, not a Server method, since it only needs the
// store and config that already exist before the Server does): for a
// file, look up its arr_items row and the owning instance's config, and
// translate monitored/cutoff/tags into a recs.Policy. This only ever
// reads tags the user set themselves in Sonarr/Radarr — nothing here
// writes anything back (that's the opt-in write-back).
func ArrPolicyFor(st *store.Store, cfg *config.Manager) func(fileID int64) *recs.Policy {
	return func(fileID int64) *recs.Policy {
		item, err := st.ArrItemByFileID(fileID)
		if err != nil || item == nil {
			return nil
		}
		var inst *config.ArrInstance
		for _, x := range cfg.Get().ArrInstances {
			if x.ID == item.InstanceID {
				x := x
				inst = &x
				break
			}
		}
		if inst == nil {
			// The owning instance was removed or disabled since the
			// last sync: no config to read toggles/tag names from, so
			// no policy to apply. A future sync will either re-claim
			// this file under a different instance or drop the row.
			return nil
		}

		p := &recs.Policy{Instance: inst.Name}
		if _, ok, _ := st.KVGet(upgradeLoopKey(fileID)); ok {
			p.UpgradeLoop = true
			return p
		}
		if inst.SkipUpgradePendingOn() && item.Monitored && item.CutoffNotMet {
			p.UpgradePending = true
			return p
		}
		if inst.TagPolicyOn() {
			tagNames := st.ArrTags(inst.ID)
			has := map[string]bool{}
			for _, id := range item.TagIDs() {
				if n, ok := tagNames[id]; ok {
					has[n] = true
				}
			}
			skipTag, av1Tag, hevcTag, remuxTag := inst.TagNames()
			switch {
			case has[skipTag]:
				p.Skip = true
			case has[remuxTag]:
				p.RemuxOnly = true
			case has[av1Tag]:
				p.Codec = "av1"
			case has[hevcTag]:
				p.Codec = "hevc"
			}
		}
		return p
	}
}
