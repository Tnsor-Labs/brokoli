package fetchers

import (
	"context"
	"errors"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

var (
	ErrUnsupportedSourceType = errors.New("unsupported source type")
)

type Fetcher interface {
	Fetch(source string, options map[string]interface{}) (*common.DataSet, error)
}

// ContextFetcher is a Fetcher that can be bounded by a context. The engine
// uses it when a fetcher offers it, so a node's deadline reaches the
// request; a Fetcher without it still works, without that bound.
type ContextFetcher interface {
	FetchContext(ctx context.Context, source string, options map[string]interface{}) (*common.DataSet, error)
}

func GetFetcher(sourceType string) (Fetcher, error) {
	switch sourceType {
	case "rest":
		return &RESTFetcher{}, nil
	// Future implementations can be added here
	// case "database":
	//     return &DatabaseFetcher{}, nil
	default:
		return nil, ErrUnsupportedSourceType
	}
}
