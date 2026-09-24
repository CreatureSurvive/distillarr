// SPDX-License-Identifier: GPL-3.0-or-later

package langprune

import (
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Result is what Select decided for one file's tracks. Keep/Drop hold
// store.AudioStream.Index / store.SubStream.Index values, not slice
// positions.
type Result struct {
	KeepAudio, DropAudio []int
	KeepSubs, DropSubs   []int
	SavedBytes           int64
}

// Select decides which audio/subtitle tracks a file's LangPolicy would
// keep or drop. Pure function, no I/O.
//
// original is the file's original-language code, already resolved by the
// caller (arr_items.original_language via NameToCode, or "" when
// unknown — Select then falls back to the first audio track's own
// language). durSec is the file's duration, used only to turn a dropped
// track's bitrate into an estimated byte count.
func Select(audio []store.AudioStream, subs []store.SubStream, original string, durSec float64, policy config.LangPolicy) Result {
	orig := Normalize(original)
	if orig == "" && len(audio) > 0 {
		orig = Normalize(audio[0].Lang)
	}

	res := Result{}
	audioOn := policy.AudioMode != "" && policy.AudioMode != "off"
	subsOn := policy.SubsMode != "" && policy.SubsMode != "off"

	wantAudio := codeSet(policy.AudioKeep)
	keepAudioIdx := map[int]bool{}
	if !audioOn {
		for _, a := range audio {
			keepAudioIdx[a.Index] = true
		}
	} else {
		lang := make([]string, len(audio))
		keep := make([]bool, len(audio))
		for i, a := range audio {
			lang[i] = Normalize(a.Lang)
			switch {
			case a.Commentary && !policy.KeepCommentary:
				keep[i] = false
			case IsUndetermined(a.Lang):
				keep[i] = policy.KeepUndeterminedOn()
			default:
				keep[i] = wantAudio[lang[i]]
			}
		}
		if policy.BestPerLanguage {
			bestByLang := map[string]int{} // lang -> best index into audio
			for i, a := range audio {
				if !keep[i] {
					continue
				}
				cur, ok := bestByLang[lang[i]]
				if !ok || audioScore(a) > audioScore(audio[cur]) {
					bestByLang[lang[i]] = i
				}
			}
			best := map[int]bool{}
			for _, i := range bestByLang {
				best[i] = true
			}
			for i := range keep {
				if keep[i] && !best[i] {
					keep[i] = false
				}
			}
		}
		if !anyTrue(keep) && len(audio) > 0 {
			// Always keep at least one audio track: prefer the original
			// language if a track has it, else the first track.
			i := 0
			for j, a := range audio {
				if Normalize(a.Lang) == orig && orig != "" {
					i = j
					break
				}
			}
			keep[i] = true
		}
		for i, a := range audio {
			if keep[i] {
				keepAudioIdx[a.Index] = true
			} else {
				res.SavedBytes += trackBytes(a.BitRate, durSec)
			}
		}
	}
	for _, a := range audio {
		if keepAudioIdx[a.Index] {
			res.KeepAudio = append(res.KeepAudio, a.Index)
		} else {
			res.DropAudio = append(res.DropAudio, a.Index)
		}
	}

	// Languages of the audio tracks that ended up kept, for the
	// "forced subtitles in kept audio languages" rule below.
	keptAudioLangs := map[string]bool{}
	for _, a := range audio {
		if keepAudioIdx[a.Index] {
			keptAudioLangs[Normalize(a.Lang)] = true
		}
	}

	wantSubs := codeSet(policy.SubsKeep)
	keepSubIdx := map[int]bool{}
	if !subsOn {
		for _, s := range subs {
			keepSubIdx[s.Index] = true
		}
	} else {
		lang := make([]string, len(subs))
		keep := make([]bool, len(subs))
		forcedKept := make([]bool, len(subs))
		for i, s := range subs {
			lang[i] = Normalize(s.Lang)
			switch {
			case s.Forced && keptAudioLangs[lang[i]]:
				keep[i] = true
				forcedKept[i] = true
			case s.SDH && !policy.KeepSDHOn():
				keep[i] = false
			case s.Commentary && !policy.KeepCommentary:
				keep[i] = false
			case IsUndetermined(s.Lang):
				keep[i] = policy.KeepUndeterminedOn()
			default:
				keep[i] = wantSubs[lang[i]]
			}
		}
		if policy.BestPerLanguage {
			bestByLang := map[string]int{}
			for i, s := range subs {
				if !keep[i] || forcedKept[i] {
					continue
				}
				cur, ok := bestByLang[lang[i]]
				if !ok || subScore(s) > subScore(subs[cur]) {
					bestByLang[lang[i]] = i
				}
			}
			best := map[int]bool{}
			for _, i := range bestByLang {
				best[i] = true
			}
			for i := range keep {
				if keep[i] && !forcedKept[i] && !best[i] {
					keep[i] = false
				}
			}
		}
		for i, s := range subs {
			if keep[i] {
				keepSubIdx[s.Index] = true
			} else {
				res.SavedBytes += trackBytes(s.BitRate, durSec)
			}
		}
	}
	for _, s := range subs {
		if keepSubIdx[s.Index] {
			res.KeepSubs = append(res.KeepSubs, s.Index)
		} else {
			res.DropSubs = append(res.DropSubs, s.Index)
		}
	}

	return res
}

func codeSet(codes []string) map[string]bool {
	out := make(map[string]bool, len(codes))
	for _, c := range codes {
		out[Normalize(c)] = true
	}
	return out
}

func anyTrue(bs []bool) bool {
	for _, b := range bs {
		if b {
			return true
		}
	}
	return false
}

// audioScore ranks audio tracks of the same language for BestPerLanguage:
// more channels wins, bitrate breaks a tie.
func audioScore(a store.AudioStream) int64 {
	return int64(a.Channels)*1_000_000_000 + a.BitRate
}

// subScore ranks subtitle tracks of the same language for
// BestPerLanguage: the default track wins, non-SDH breaks a tie.
func subScore(s store.SubStream) int {
	score := 0
	if s.Default {
		score += 2
	}
	if !s.SDH {
		score++
	}
	return score
}

// trackBytes estimates a dropped track's size from its bitrate (bits/s)
// and the file's duration. Text subtitle tracks report no bitrate, so
// they contribute 0 — same "no data, no estimate" tradeoff as elsewhere
// in this codebase's size estimation.
func trackBytes(bitRate int64, durSec float64) int64 {
	if bitRate <= 0 || durSec <= 0 {
		return 0
	}
	return int64(float64(bitRate) * durSec / 8)
}
