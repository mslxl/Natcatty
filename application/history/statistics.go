package history

import (
	"context"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// Statistics summarizes the live Command History rows matching filter. It is
// the same result set the History surface lists: soft-deleted rows are excluded
// by the Store, and the filter is applied here so the summary and the list
// share one definition. It is a separate query slice from search and paging;
// callers that already hold the filtered rows may use
// domainhistory.ComputeStatistics directly.
func (s *Service) Statistics(ctx context.Context, filter domainhistory.EntryFilter) (domainhistory.Statistics, error) {
	entries, err := s.Load(ctx)
	if err != nil {
		return domainhistory.Statistics{}, err
	}
	matched := make([]domainhistory.Entry, 0, len(entries))
	for _, entry := range entries {
		if filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	return domainhistory.ComputeStatistics(matched), nil
}
