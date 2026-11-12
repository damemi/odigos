package spec

import "fmt"

// Resolve determines the authentication path a provided credential set selects, returned base-first
// (e.g. [static-keys, assume-role], [workload-identity], or [workload-identity, assume-role]). The map
// is keyed by auth field name (auth.methods[].fields[].name); empty values are treated as absent.
//
// It validates that:
//   - every provided key is a declared auth field,
//   - each engaged method (one with a provided field) has all of its required fields,
//   - a composed method (one with baseMethods) has a satisfiable base, and
//   - no provided method is left unused by the resolved chain.
//
// It is provider-agnostic and pure (no live calls), so test-connection can verify every declared
// auth mode and reject invalid credential combinations before contacting the provider.
func (a AuthConfig) Resolve(provided map[string]string) ([]AuthMethod, error) {
	byID := make(map[string]AuthMethod, len(a.Methods))
	fieldOwner := map[string]string{}
	for _, m := range a.Methods {
		byID[m.ID] = m
		for _, f := range m.Fields {
			fieldOwner[f.Name] = m.ID
		}
	}

	present := map[string]bool{}
	engaged := map[string]bool{}
	for k, v := range provided {
		if v == "" {
			continue
		}
		owner, ok := fieldOwner[k]
		if !ok {
			return nil, fmt.Errorf("unknown credential field %q", k)
		}
		present[k] = true
		engaged[owner] = true
	}

	// Every engaged method must have all of its required fields.
	for id := range engaged {
		for _, f := range byID[id].Fields {
			if f.Required && !present[f.Name] {
				return nil, fmt.Errorf("auth method %q requires field %q", id, f.Name)
			}
		}
	}

	// Partition engaged methods: composed (declares baseMethods) vs standalone base methods.
	var composed []AuthMethod
	var standalone []string
	for id := range engaged {
		if len(byID[id].BaseMethods) > 0 {
			composed = append(composed, byID[id])
		} else {
			standalone = append(standalone, id)
		}
	}
	if len(composed) > 1 {
		return nil, fmt.Errorf("ambiguous credentials: multiple composed auth methods engaged")
	}
	if len(standalone) > 1 {
		return nil, fmt.Errorf("ambiguous credentials: multiple base auth methods engaged")
	}

	var chain []AuthMethod
	switch {
	case len(composed) == 1:
		top := composed[0]
		base, ok := a.resolveBase(top, byID, engaged)
		if !ok {
			return nil, fmt.Errorf("auth method %q requires one of its base methods %v to be provided", top.ID, top.BaseMethods)
		}
		chain = []AuthMethod{base, top}
	case len(standalone) == 1:
		chain = []AuthMethod{byID[standalone[0]]}
	default:
		// Nothing engaged: fall back to an ambient base method (one with no fields, e.g. workload identity).
		base, ok := a.ambientBase()
		if !ok {
			return nil, fmt.Errorf("no credentials provided and no ambient auth method is declared")
		}
		chain = []AuthMethod{base}
	}

	// Every engaged method must be part of the resolved chain (no unused credentials).
	inChain := map[string]bool{}
	for _, m := range chain {
		inChain[m.ID] = true
	}
	for id := range engaged {
		if !inChain[id] {
			return nil, fmt.Errorf("credentials for auth method %q cannot be combined as provided", id)
		}
	}
	return chain, nil
}

// resolveBase picks a satisfiable base for a composed method: an engaged base method listed in its
// baseMethods, else an ambient (no-field) base method listed there.
func (a AuthConfig) resolveBase(top AuthMethod, byID map[string]AuthMethod, engaged map[string]bool) (AuthMethod, bool) {
	for _, bid := range top.BaseMethods {
		if bm, ok := byID[bid]; ok && engaged[bid] {
			return bm, true
		}
	}
	for _, bid := range top.BaseMethods {
		if bm, ok := byID[bid]; ok && len(bm.Fields) == 0 {
			return bm, true
		}
	}
	return AuthMethod{}, false
}

// ambientBase returns the first declared standalone method with no fields (ambient identity).
func (a AuthConfig) ambientBase() (AuthMethod, bool) {
	for _, m := range a.Methods {
		if len(m.BaseMethods) == 0 && len(m.Fields) == 0 {
			return m, true
		}
	}
	return AuthMethod{}, false
}
