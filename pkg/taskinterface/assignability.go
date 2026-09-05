package taskinterface

import (
	"fmt"
	"math/big"
)

// Verdict is the outcome of a directional assignability check (ADR-032
// section 9). Unverified is a first-class outcome, not a fallback error:
// "it is never displayed as statically compatible" (ADR-032 section 9) --
// callers must not treat Unverified as Assignable.
type Verdict string

const (
	Assignable   Verdict = "assignable"
	Incompatible Verdict = "incompatible"
	Unverified   Verdict = "unverified"
)

// Result is one assignability outcome, carrying enough to reproduce
// ADR-032 section 9's worked diagnostic example:
//
//	publish.orders <- score_orders.result: required field $.score expects
//	float64 but producer declares string
//
// Path uses "$" for the port root and dotted/bracket JSON-Pointer-ish
// segments below it (e.g. "$.score", "$.items[].id"); callers building a
// full edge diagnostic prepend "<consumer node>.<port> <- <producer
// node>.<port>: " themselves -- this package has no concept of nodes or
// edges.
type Result struct {
	Verdict Verdict
	Path    string
	Reason  string
}

func ok() Result { return Result{Verdict: Assignable} }

func incompatible(path, format string, args ...interface{}) Result {
	return Result{Verdict: Incompatible, Path: path, Reason: fmt.Sprintf(format, args...)}
}

func unverified(path, format string, args ...interface{}) Result {
	return Result{Verdict: Unverified, Path: path, Reason: fmt.Sprintf(format, args...)}
}

// worst combines two results, keeping the more severe verdict
// (Incompatible > Unverified > Assignable) and its diagnostic. Used when
// checking several independent sub-parts of one type (e.g. every record
// field) and reporting the first/worst failure.
func worst(a, b Result) Result {
	rank := map[Verdict]int{Assignable: 0, Unverified: 1, Incompatible: 2}
	if rank[b.Verdict] > rank[a.Verdict] {
		return b
	}
	return a
}

// AssignPort checks whether a value a producer port emits may satisfy a
// consumer port, per ADR-032 section 9. Both ports' own cardinality is a
// graph-level (edge-count) concern, not checked here.
func AssignPort(producer, consumer PortValue) Result {
	if producer.Kind != consumer.Kind {
		return incompatible("$", "value kind mismatch: producer is %q, consumer is %q (rule 1: value kinds must match except via an explicit conversion node)", producer.Kind, consumer.Kind)
	}
	switch producer.Kind {
	case ValueDataset:
		return assignRow(producer.Row, consumer.Row)
	case ValueScalar:
		return AssignType(*producer.ScalarType, *consumer.ScalarType, "$")
	case ValueArtifact:
		return assignMediaTypes(producer.MediaTypes, consumer.MediaTypes)
	case ValueCollection:
		return AssignPort(*producer.Items, *consumer.Items)
	case ValueControl:
		return ok() // no business value; kind match above is the whole check
	default:
		return unverified("$", "unrecognized value kind %q", producer.Kind)
	}
}

// assignRow implements the dataset 'row' special case: an absent row
// descriptor means unknown (ADR-032 section 6), and an unknown producer
// cannot statically prove a closed consumer (section 9 rule 3), while an
// unknown/absent consumer accepts any producer row (a consumer that
// declared no row shape is explicitly not asserting one).
func assignRow(producer, consumer *Type) Result {
	if consumer == nil {
		return ok() // consumer doesn't declare a row shape at all: nothing to fail
	}
	if producer == nil {
		if consumer.Kind == KindUnknown {
			return ok() // both sides unknown, rule 12's principle applied to rows
		}
		return unverified("$", "producer's row shape is unknown; consumer expects a concrete schema (rule 3: an unknown producer does not statically prove a closed consumer)")
	}
	return AssignType(*producer, *consumer, "$")
}

func assignMediaTypes(producer, consumer []string) Result {
	if len(consumer) == 0 {
		return ok() // consumer accepts any media type
	}
	if len(producer) == 0 {
		return unverified("$", "producer does not declare which media types it emits; consumer accepts only %v", consumer)
	}
	accepted := map[string]bool{}
	for _, mt := range consumer {
		accepted[mt] = true
	}
	for _, mt := range producer {
		if !accepted[mt] {
			return incompatible("$", "producer may emit media type %q, which consumer does not accept (accepts %v)", mt, consumer)
		}
	}
	return ok()
}

// AssignType is the recursive BPTD type-level check (ADR-032 section 9).
// path names the position being checked, for diagnostics.
func AssignType(producer, consumer Type, path string) Result {
	if producer.Kind == KindUnknown && consumer.Kind == KindUnknown {
		return ok() // rule 12
	}
	if producer.Kind == KindUnknown || consumer.Kind == KindUnknown {
		return unverified(path, "one side's type is unknown (rule 12: unknown yields unverified except when both sides are unknown)")
	}
	if producer.Kind != consumer.Kind {
		return incompatible(path, "producer declares %q, consumer expects %q (rule 1; rule 5 for the specific int64->float64 case: numeric widening is never implicit)", producer.Kind, consumer.Kind)
	}
	// rule 4: nullable values are assignable only to nullable consumers.
	if producer.Nullable && !consumer.Nullable {
		return incompatible(path, "producer is nullable but consumer is not (rule 4)")
	}

	switch producer.Kind {
	case KindArray:
		res := AssignType(*producer.Items, *consumer.Items, path+"[]")
		return worst(res, assignArrayConstraints(producer, consumer, path))
	case KindMap:
		return AssignType(*producer.Items, *consumer.Items, path+"{}") // map keys are always string (ADR-032 section 4)
	case KindRecord:
		return assignRecord(producer, consumer, path)
	case KindEnum:
		return assignEnum(producer, consumer, path)
	case KindUnion:
		return assignUnion(producer, consumer, path)
	case KindString:
		return assignStringConstraints(producer, consumer, path)
	case KindInt64, KindDecimal:
		return assignCanonicalNumericConstraints(producer, consumer, path)
	case KindFloat64:
		return assignFloatConstraints(producer, consumer, path)
	case KindJSON:
		return ok() // rule 12: json only assignable to json, already enforced by the kind-match check above
	default:
		// boolean, bytes, date, timestamp, duration: no declarable
		// constraints in this schema version, so kind + nullable
		// (already checked) is the whole story.
		return ok()
	}
}

// assignRecord implements rule 2 (structural field matching) and the
// "producer extras are allowed only when the consumer is open" clause.
func assignRecord(producer, consumer Type, path string) Result {
	producerFields := map[string]Field{}
	for _, f := range producer.Fields {
		producerFields[f.Name] = f
	}
	consumerNames := map[string]bool{}
	result := ok()

	for _, cf := range consumer.Fields {
		consumerNames[cf.Name] = true
		pf, present := producerFields[cf.Name]
		fieldPath := path + "." + cf.Name
		if !present {
			if cf.Required {
				result = worst(result, incompatible(fieldPath, "required field %s is not declared by the producer", fieldPath))
			}
			continue // optional consumer field the producer doesn't have: nothing flows, fine
		}
		if cf.Required && !pf.Required {
			result = worst(result, incompatible(fieldPath, "required field %s is only optional on the producer (rule 2: optional producer fields cannot satisfy required consumer fields)", fieldPath))
			continue
		}
		result = worst(result, AssignType(pf.Type, cf.Type, fieldPath))
	}

	if !consumer.AdditionalFields {
		for _, pf := range producer.Fields {
			if !consumerNames[pf.Name] {
				result = worst(result, incompatible(path+"."+pf.Name, "producer declares field %q that the consumer's closed record does not (rule 2: producer extras are allowed only when the consumer is open)", pf.Name))
			}
		}
	}
	return result
}

// assignEnum implements rule 6: producer's value set must be a subset of
// the consumer's.
func assignEnum(producer, consumer Type, path string) Result {
	accepted := map[string]bool{}
	for _, v := range consumer.Values {
		accepted[v] = true
	}
	for _, v := range producer.Values {
		if !accepted[v] {
			return incompatible(path, "producer enum value %q is not in the consumer's accepted set %v (rule 6)", v, consumer.Values)
		}
	}
	return ok()
}

// assignUnion implements rule 10: every producer variant must have a
// matching consumer tag with an assignable payload.
func assignUnion(producer, consumer Type, path string) Result {
	consumerVariants := map[string]Type{}
	for _, v := range consumer.Variants {
		consumerVariants[v.Tag] = v.Type
	}
	result := ok()
	for _, pv := range producer.Variants {
		cv, present := consumerVariants[pv.Tag]
		variantPath := fmt.Sprintf("%s[%s=%s]", path, consumer.TagField, pv.Tag)
		if !present {
			result = worst(result, incompatible(variantPath, "producer variant tag %q has no matching consumer variant (rule 10)", pv.Tag))
			continue
		}
		result = worst(result, AssignType(pv.Type, cv, variantPath))
	}
	return result
}

// assignStringConstraints implements the string half of rule 11:
// min_length/max_length must be a subset, and pattern is assignable only
// when identical -- otherwise unverified (stated as literally that in
// ADR-032 section 9 rule 11, unlike the other constraint checks, which
// are incompatible on violation).
func assignStringConstraints(producer, consumer Type, path string) Result {
	result := ok()
	if consumer.MinLength != nil {
		if producer.MinLength == nil || *producer.MinLength < *consumer.MinLength {
			result = worst(result, incompatible(path, "producer's min_length (%s) does not imply the consumer's (%d) (rule 11)", intPtrString(producer.MinLength), *consumer.MinLength))
		}
	}
	if consumer.MaxLength != nil {
		if producer.MaxLength == nil || *producer.MaxLength > *consumer.MaxLength {
			result = worst(result, incompatible(path, "producer's max_length (%s) does not imply the consumer's (%d) (rule 11)", intPtrString(producer.MaxLength), *consumer.MaxLength))
		}
	}
	if consumer.Pattern != nil {
		if producer.Pattern == nil || *producer.Pattern != *consumer.Pattern {
			result = worst(result, unverified(path, "producer and consumer patterns are not identical (rule 11: patterns are assignable only when identical; otherwise unverified)"))
		}
	}
	return result
}

func intPtrString(p *int) string {
	if p == nil {
		return "unset"
	}
	return fmt.Sprintf("%d", *p)
}

// assignArrayConstraints checks min_items/max_items/unique_items subset
// (rule 11's principle applied to arrays, not separately named but
// following the same "producer constraints must imply consumer
// constraints" rule).
func assignArrayConstraints(producer, consumer Type, path string) Result {
	result := ok()
	if consumer.MinItems != nil {
		if producer.MinItems == nil || *producer.MinItems < *consumer.MinItems {
			result = worst(result, incompatible(path, "producer's min_items (%s) does not imply the consumer's (%d) (rule 11)", intPtrString(producer.MinItems), *consumer.MinItems))
		}
	}
	if consumer.MaxItems != nil {
		if producer.MaxItems == nil || *producer.MaxItems > *consumer.MaxItems {
			result = worst(result, incompatible(path, "producer's max_items (%s) does not imply the consumer's (%d) (rule 11)", intPtrString(producer.MaxItems), *consumer.MaxItems))
		}
	}
	if consumer.UniqueItems && !producer.UniqueItems {
		result = worst(result, unverified(path, "consumer requires unique_items but the producer does not declare that guarantee"))
	}
	return result
}

// assignCanonicalNumericConstraints checks int64/decimal minimum/maximum/
// exclusive_minimum/exclusive_maximum subset (rule 11), parsing the
// canonical string encodings via big.Float.
func assignCanonicalNumericConstraints(producer, consumer Type, path string) Result {
	result := ok()
	check := func(label string, consumerBound *string, cmp func(p, c *big.Float) bool, msg string) {
		if consumerBound == nil {
			return
		}
		cf, err := parseCanonicalNumber(*consumerBound)
		if err != nil {
			result = worst(result, unverified(path, "consumer %s %q is not a parseable canonical number", label, *consumerBound))
			return
		}
		var pf *big.Float
		var producerBound *string
		switch label {
		case "minimum":
			producerBound = producer.Minimum
		case "maximum":
			producerBound = producer.Maximum
		case "exclusive_minimum":
			producerBound = producer.ExclusiveMinimum
		case "exclusive_maximum":
			producerBound = producer.ExclusiveMaximum
		}
		if producerBound == nil {
			result = worst(result, unverified(path, "producer declares no %s; consumer requires %s %s (rule 11)", label, label, *consumerBound))
			return
		}
		pf, err = parseCanonicalNumber(*producerBound)
		if err != nil {
			result = worst(result, unverified(path, "producer %s %q is not a parseable canonical number", label, *producerBound))
			return
		}
		if !cmp(pf, cf) {
			result = worst(result, incompatible(path, "%s", msg))
		}
	}
	check("minimum", consumer.Minimum, func(p, c *big.Float) bool { return p.Cmp(c) >= 0 },
		fmt.Sprintf("producer's minimum (%s) does not imply the consumer's (%s) (rule 11)", derefStr(producer.Minimum), derefStr(consumer.Minimum)))
	check("maximum", consumer.Maximum, func(p, c *big.Float) bool { return p.Cmp(c) <= 0 },
		fmt.Sprintf("producer's maximum (%s) does not imply the consumer's (%s) (rule 11)", derefStr(producer.Maximum), derefStr(consumer.Maximum)))
	check("exclusive_minimum", consumer.ExclusiveMinimum, func(p, c *big.Float) bool { return p.Cmp(c) >= 0 },
		fmt.Sprintf("producer's exclusive_minimum (%s) does not imply the consumer's (%s) (rule 11)", derefStr(producer.ExclusiveMinimum), derefStr(consumer.ExclusiveMinimum)))
	check("exclusive_maximum", consumer.ExclusiveMaximum, func(p, c *big.Float) bool { return p.Cmp(c) <= 0 },
		fmt.Sprintf("producer's exclusive_maximum (%s) does not imply the consumer's (%s) (rule 11)", derefStr(producer.ExclusiveMaximum), derefStr(consumer.ExclusiveMaximum)))
	return result
}

func derefStr(p *string) string {
	if p == nil {
		return "unset"
	}
	return *p
}

// assignFloatConstraints is assignCanonicalNumericConstraints's float64
// counterpart (plain float64 bounds, not canonical strings).
func assignFloatConstraints(producer, consumer Type, path string) Result {
	result := ok()
	check := func(label string, producerBound, consumerBound *float64, cmp func(p, c float64) bool) {
		if consumerBound == nil {
			return
		}
		if producerBound == nil {
			result = worst(result, unverified(path, "producer declares no %s; consumer requires one (rule 11)", label))
			return
		}
		if !cmp(*producerBound, *consumerBound) {
			result = worst(result, incompatible(path, "producer's %s (%v) does not imply the consumer's (%v) (rule 11)", label, *producerBound, *consumerBound))
		}
	}
	check("minimum", producer.MinimumF, consumer.MinimumF, func(p, c float64) bool { return p >= c })
	check("maximum", producer.MaximumF, consumer.MaximumF, func(p, c float64) bool { return p <= c })
	check("exclusive_minimum", producer.ExclusiveMinimumF, consumer.ExclusiveMinimumF, func(p, c float64) bool { return p >= c })
	check("exclusive_maximum", producer.ExclusiveMaximumF, consumer.ExclusiveMaximumF, func(p, c float64) bool { return p <= c })
	return result
}
