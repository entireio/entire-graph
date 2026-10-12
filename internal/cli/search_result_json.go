package cli

import (
	"encoding/json"
	"github.com/entireio/entire-graph/internal/sem"
)

// An embedded SearchResult would promote its MarshalJSON and hide record_type.
type searchResultRecord struct{ Result sem.SearchResult }

func (record searchResultRecord) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(record.Result)
	if err != nil {
		return nil, err
	}
	return append([]byte(`{"record_type":"search_result",`), body[1:]...), nil
}
