package upi

import (
	"hash/fnv"
	"math"
)

// seededRand is a tiny deterministic PRNG (xorshift64) seeded from the request
// phone, so weighted source sampling is reproducible per request (and in tests)
// while still varying across phones. It stands in for numpy's RandomState.
type seededRand struct{ s uint64 }

func newSeededRand(seed string) *seededRand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	v := h.Sum64()
	if v == 0 {
		v = 0x9e3779b97f4a7c15
	}
	return &seededRand{s: v}
}

func (r *seededRand) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

// float64 in [0,1).
func (r *seededRand) f() float64 {
	return float64(r.next()>>11) / float64(1<<53)
}

// weightedSampleNoReplace picks k unique items from sources with probability
// proportional to weights, without replacement (numpy random.choice replace=
// False, p=weights). Uses the Efraimidis–Spirakis weighted reservoir key
// (w^(1/weight)) so the draw is weighted and unique. If a weight is <=0 it is
// treated as a tiny epsilon so the item can still (rarely) be chosen, matching
// numpy's behavior of allowing zero-weight only when it wouldn't force a pick.
func weightedSampleNoReplace(sources []SourceConfig, weights []float64, k int, rng *seededRand) []SourceConfig {
	n := len(sources)
	if k <= 0 || n == 0 {
		return nil
	}
	if k >= n {
		// Return all, de-duplicated by name (Python builds a name->source dict).
		return dedupByName(sources)
	}
	type keyed struct {
		src SourceConfig
		key float64
	}
	keys := make([]keyed, n)
	for i := 0; i < n; i++ {
		w := weights[i]
		if w <= 0 {
			w = 1e-9
		}
		u := rng.f()
		if u <= 0 {
			u = 1e-12
		}
		// key = u^(1/w); larger key = more likely to be selected.
		keys[i] = keyed{src: sources[i], key: pow(u, 1.0/w)}
	}
	// selection = top-k by key.
	for i := 0; i < k; i++ {
		maxIdx := i
		for j := i + 1; j < n; j++ {
			if keys[j].key > keys[maxIdx].key {
				maxIdx = j
			}
		}
		keys[i], keys[maxIdx] = keys[maxIdx], keys[i]
	}
	picked := make([]SourceConfig, 0, k)
	for i := 0; i < k; i++ {
		picked = append(picked, keys[i].src)
	}
	return dedupByName(picked)
}

func dedupByName(sources []SourceConfig) []SourceConfig {
	seen := map[string]struct{}{}
	out := make([]SourceConfig, 0, len(sources))
	for _, s := range sources {
		if _, ok := seen[s.Name]; ok {
			continue
		}
		seen[s.Name] = struct{}{}
		out = append(out, s)
	}
	return out
}

func pow(x, y float64) float64 { return math.Pow(x, y) }
