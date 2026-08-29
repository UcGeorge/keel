package cloudserver

import (
	"context"
	"log/slog"

	"github.com/UcGeorge/keel/internal/config"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/UcGeorge/keel/internal/web"
	"github.com/google/uuid"
)

// storeRunInputs snapshots the values a run starts with. Non-secret values
// are encrypted at rest; secrets record only that a value was set.
func (s *Server) storeRunInputs(ctx context.Context, runID uuid.UUID, d *config.Deployment, values map[string]string) {
	for _, in := range web.SnapshotInputs(d, values) {
		var enc []byte
		if in.Value != "" {
			var err error
			if enc, err = s.Box.SealString(in.Value); err != nil {
				slog.Error("seal run input", "run", runID, "input", in.Name, "err", err)
				continue
			}
		}
		if err := s.Q.InsertRunInput(ctx, clouddb.InsertRunInputParams{
			RunID: runID, Idx: int32(in.Idx), Name: in.Name, Label: in.Label,
			ValueEnc: enc, IsSecret: in.Secret, DeployTime: in.DeployTime, Source: in.Source,
		}); err != nil {
			slog.Error("store run input", "run", runID, "input", in.Name, "err", err)
		}
	}
}

// runInputVMs loads and decrypts a run's inputs in declaration order.
func (s *Server) runInputVMs(ctx context.Context, runID uuid.UUID) []web.RunInputVM {
	rows, err := s.Q.ListRunInputs(ctx, runID)
	if err != nil {
		return nil
	}
	out := make([]web.RunInputVM, 0, len(rows))
	for _, row := range rows {
		vm := web.RunInputVM{Name: row.Name, Label: row.Label, Secret: row.IsSecret, DeployTime: row.DeployTime, Source: row.Source}
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
	ids := make([]uuid.UUID, 0, len(runs))
	index := map[string]int{}
	for i, r := range runs {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
			index[r.ID] = i
		}
	}
	rows, err := s.Q.ListRunDeployInputsForRuns(ctx, ids)
	if err != nil {
		return
	}
	for _, row := range rows {
		i, ok := index[row.RunID.String()]
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
