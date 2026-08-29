package devserver

import (
	"context"
	"log/slog"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/store/devdb"
	"github.com/UcGeorge/keel/internal/web"
)

// storeRunInputs snapshots the values a run starts with. Non-secret values
// are encrypted at rest; secrets record only that a value was set.
func (s *Server) storeRunInputs(ctx context.Context, runID string, d *config.Deployment, values map[string]string) {
	for _, in := range web.SnapshotInputs(d, values) {
		var enc []byte
		if in.Value != "" {
			var err error
			if enc, err = s.Box.SealString(in.Value); err != nil {
				slog.Error("seal run input", "run", runID, "input", in.Name, "err", err)
				continue
			}
		}
		if err := s.Q.InsertRunInput(ctx, devdb.InsertRunInputParams{
			RunID: runID, Idx: int64(in.Idx), Name: in.Name, Label: in.Label,
			ValueEnc: enc, IsSecret: boolInt(in.Secret), DeployTime: boolInt(in.DeployTime), Source: in.Source,
		}); err != nil {
			slog.Error("store run input", "run", runID, "input", in.Name, "err", err)
		}
	}
}

// runInputVMs loads and decrypts a run's inputs in declaration order.
func (s *Server) runInputVMs(ctx context.Context, runID string) []web.RunInputVM {
	rows, err := s.Q.ListRunInputs(ctx, runID)
	if err != nil {
		return nil
	}
	out := make([]web.RunInputVM, 0, len(rows))
	for _, row := range rows {
		vm := web.RunInputVM{Name: row.Name, Label: row.Label, Secret: row.IsSecret != 0, DeployTime: row.DeployTime != 0, Source: row.Source}
		if len(row.ValueEnc) > 0 {
			if v, err := s.Box.OpenString(row.ValueEnc); err == nil {
				vm.Value = v
			}
		}
		out = append(out, vm)
	}
	return out
}

// attachRunInputChips decorates table rows with their deploy-time values
// in one query.
func (s *Server) attachRunInputChips(ctx context.Context, runs []web.RunVM) {
	if len(runs) == 0 {
		return
	}
	ids := make([]string, len(runs))
	index := map[string]int{}
	for i, r := range runs {
		ids[i] = r.ID
		index[r.ID] = i
	}
	rows, err := s.Q.ListRunDeployInputsForRuns(ctx, ids)
	if err != nil {
		return
	}
	for _, row := range rows {
		i, ok := index[row.RunID]
		if !ok || len(row.ValueEnc) == 0 {
			continue
		}
		v, err := s.Box.OpenString(row.ValueEnc)
		if err != nil {
			continue
		}
		runs[i].Inputs = append(runs[i].Inputs, web.RunInputChip{Name: row.Name, Value: v})
	}
}
