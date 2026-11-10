package learning

// Default thresholds for live-traffic URL templatization rule learning.
// defaultMinObservationsForRule and defaultMinCardinalityForTemplating match
// cardinalityControl.urlTemplatization.liveTrafficLearning.ruleComputation.
const defaultMinObservationsForRule int64 = 100

const (
	defaultMaxRecurseEachChildren      = 5
	defaultMaxWildcardLowChildren      = 10
	defaultMinCardinalityForTemplating = 20
	defaultMaxTemplatedExamples        = 5

	// templateChildrenWhenStatic is the distinct-child count that becomes "{id}"
	// when the average child template probability is 0.
	templateChildrenWhenStatic = 50
)

// LiveTrafficLearningParams controls numeric thresholds for
// FindLiveTrafficLearningRules. Nil (unset) fields use the package defaults.
type LiveTrafficLearningParams struct {
	// MinObservationsForRule is the observation count required before any rule
	// is considered. The count can come from one path or from several distinct
	// paths. Until it exceeds this value, no rule is created and learning waits
	// for more observations. Matches ruleComputation.minObservationsForRule.
	// Default 100.
	MinObservationsForRule *int64

	// MinCardinalityForTemplating is how many distinct children become "{id}"
	// when they look like parameters. Static-looking children need 50.
	// Default 20.
	MinCardinalityForTemplating *int

	// MaxRecurseEachChildren is the max distinct child segment count for which
	// each child is walked independently (after the single-child case). Above
	// this, children are merged into a wildcard or templated segment.
	// Band: 2..MaxRecurseEachChildren inclusive. Default 5.
	MaxRecurseEachChildren *int

	// MaxWildcardLowChildren is the max distinct child count for a "*" segment
	// with moderate certainty. Band: (MaxRecurseEachChildren+1)..this inclusive.
	// Default 10.
	MaxWildcardLowChildren *int

	// MaxTemplatedExamples is how many distinct child segment values are kept as
	// examples on a learned templated/wildcard segment. Default 5.
	MaxTemplatedExamples *int
}

// resolvedLearningParams is LiveTrafficLearningParams with defaults applied.
type resolvedLearningParams struct {
	minObservationsForRule      int64
	maxRecurseEachChildren      int
	maxWildcardLowChildren      int
	minCardinalityForTemplating int
	maxTemplatedExamples        int
}

func (p LiveTrafficLearningParams) withDefaults() resolvedLearningParams {
	return resolvedLearningParams{
		minObservationsForRule:      resolvePositiveInt64(p.MinObservationsForRule, defaultMinObservationsForRule),
		maxRecurseEachChildren:      resolvePositiveInt(p.MaxRecurseEachChildren, defaultMaxRecurseEachChildren),
		maxWildcardLowChildren:      resolvePositiveInt(p.MaxWildcardLowChildren, defaultMaxWildcardLowChildren),
		minCardinalityForTemplating: resolvePositiveInt(p.MinCardinalityForTemplating, defaultMinCardinalityForTemplating),
		maxTemplatedExamples:        resolvePositiveInt(p.MaxTemplatedExamples, defaultMaxTemplatedExamples),
	}
}

func resolvePositiveInt64(v *int64, fallback int64) int64 {
	if v == nil || *v <= 0 {
		return fallback
	}
	return *v
}

func resolvePositiveInt(v *int, fallback int) int {
	if v == nil || *v <= 0 {
		return fallback
	}
	return *v
}
