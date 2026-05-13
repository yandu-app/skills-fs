package core

// inlineParams bounds the number of path parameters carried without heap
// allocation. Real mount patterns very rarely exceed this; if a router insert
// would, the configuration is rejected so the runtime never allocates.
const inlineParams = 8

// ParamSet stores path parameters extracted by the router. The zero value is
// ready to use. ParamSet is intentionally a value type so callers can pass it
// without forcing a heap allocation, satisfying the P-01 zero-allocation
// resolve budget.
type ParamSet struct {
	slots [inlineParams]paramPair
	count int
}

type paramPair struct {
	key, value string
}

func (p *ParamSet) set(key, value string) {
	if p.count >= inlineParams {
		return
	}
	p.slots[p.count] = paramPair{key: key, value: value}
	p.count++
}

// Reset clears the set so the buffer can be reused.
func (p *ParamSet) Reset() {
	p.count = 0
}

// Len returns the number of stored parameters.
func (p ParamSet) Len() int { return p.count }

// Get returns the value for key, if present.
func (p ParamSet) Get(key string) (string, bool) {
	for i := 0; i < p.count; i++ {
		if p.slots[i].key == key {
			return p.slots[i].value, true
		}
	}
	return "", false
}

// Each calls fn for every stored pair in insertion order.
func (p ParamSet) Each(fn func(k, v string)) {
	for i := 0; i < p.count; i++ {
		fn(p.slots[i].key, p.slots[i].value)
	}
}

// ToMap returns a freshly allocated map mirroring the parameter set. Callers
// that need map semantics (notably ParamsFn) should call this only after the
// hot resolve path completes.
func (p ParamSet) ToMap() map[string]string {
	if p.count == 0 {
		return nil
	}
	m := make(map[string]string, p.count)
	for i := 0; i < p.count; i++ {
		m[p.slots[i].key] = p.slots[i].value
	}
	return m
}
