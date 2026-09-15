package value

// Wire nesting budget, mirroring monty-proto's per-shape accounting.
const (
	ProstRecursionLimit = 100
	FrameWrapperDepth   = 3
	MaxProtoValueDepth  = ProstRecursionLimit - FrameWrapperDepth
	MaxValueDepth       = (MaxProtoValueDepth - 1) / 2

	listCost              = 2
	dictCost              = 3
	classInstanceCost     = 4
	typeCost              = 2
	classInstanceTypeCost = 3
	typeAttrsCost         = 2
)

// ExceedsMaxDepth reports whether a normalized value nests deeper than the wire allows.
func ExceedsMaxDepth(v any) bool { return depthExceeds(v, MaxProtoValueDepth) }

func depthExceeds(v any, budget int) bool {
	switch x := v.(type) {
	case []any:
		return seqExceeds(x, budget, listCost)
	case Tuple:
		return seqExceeds(x, budget, listCost)
	case *Set:
		return seqExceeds(x.Items(), budget, listCost)
	case *FrozenSet:
		return seqExceeds(x.Items(), budget, listCost)
	case NamedTuple:
		return seqExceeds(x.Values, budget, listCost)
	case *Dict:
		return pairsExceed(x.Pairs(), budget, dictCost)
	case Instance:
		return budget < classInstanceTypeCost || classTypeExceeds(x.Type, budget-classInstanceTypeCost) ||
			pairsExceed(x.Attrs.Pairs(), budget, classInstanceCost)
	case *Instance:
		return depthExceeds(*x, budget)
	case Type:
		if x.Origin == OriginBuiltin {
			return budget < typeCost
		}
		return budget < typeCost || classTypeExceeds(x, budget-typeCost)
	default:
		return budget == 0
	}
}

func seqExceeds(items []any, budget, cost int) bool {
	if budget < cost {
		return true
	}
	for _, it := range items {
		if depthExceeds(it, budget-cost) {
			return true
		}
	}
	return false
}

func pairsExceed(pairs []Pair, budget, cost int) bool {
	if budget < cost {
		return true
	}
	for _, p := range pairs {
		if depthExceeds(p.Key, budget-cost) || depthExceeds(p.Value, budget-cost) {
			return true
		}
	}
	return false
}

func classTypeExceeds(t Type, budget int) bool {
	return t.Attrs.Len() > 0 && pairsExceed(t.Attrs.Pairs(), budget, typeAttrsCost)
}
