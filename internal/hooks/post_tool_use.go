package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPostToolUse implements the tool-usage log (spec: seguimiento de uso de
// tools). Antes no existía ningún registro persistente de qué tools se
// usaban — Activity.RecordSignificantAction (internal/mcp/activity.go)
// estaba pensada para esto pero nunca se llamaba desde ningún lado, porque
// PostToolUse solo estaba cableado a code-review-graph, nunca a kronos.
func RunPostToolUse(ctx context.Context, in Input, st store.Storer) error {
	if in.SessionID == "" || in.ToolName == "" {
		return nil
	}
	proj := project.Detect(in.CWD)
	return st.RecordToolUse(ctx, in.SessionID, proj.Name, in.ToolName)
}
