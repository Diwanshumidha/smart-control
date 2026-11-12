// Package activity records every tool call in SQLite and on the event bus so
// the desktop app can show what the AI did, live and historically.
package activity

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

func init() {
	store.Register(store.Migration{Version: 100, Name: "tool_calls", SQL: `
CREATE TABLE tool_calls (
  id          TEXT PRIMARY KEY,
  toolset     TEXT NOT NULL,
  tool        TEXT NOT NULL,
  args        TEXT NOT NULL,
  status      TEXT NOT NULL,      -- running | ok | error
  result      TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  finished_at INTEGER,
  duration_ms INTEGER
);
CREATE INDEX tool_calls_started ON tool_calls(started_at DESC);`})
}

type Entry struct {
	ID         string          `json:"id"`
	Toolset    string          `json:"toolset"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"`
	Result     string          `json:"result"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
}

type Recorder struct {
	db  *sql.DB
	bus *events.Bus
}

func NewRecorder(db *sql.DB, bus *events.Bus) *Recorder { return &Recorder{db: db, bus: bus} }

// Middleware records start and finish of every tool call.
func (r *Recorder) Middleware() mcpx.Middleware {
	return func(next mcpx.Handler) mcpx.Handler {
		return func(ctx context.Context, call *mcpx.Call) (*mcp.CallToolResult, error) {
			e := Entry{
				ID: store.NewID("call"), Toolset: call.Toolset, Tool: call.Tool,
				Args: redactArgs(call.Args), Status: "running", StartedAt: store.Now(),
			}
			_, _ = r.db.Exec(`INSERT INTO tool_calls(id, toolset, tool, args, status, started_at) VALUES (?,?,?,?,?,?)`,
				e.ID, e.Toolset, e.Tool, string(e.Args), e.Status, e.StartedAt)
			r.bus.Publish(events.ToolStarted, e)

			start := time.Now()
			res, err := next(ctx, call)

			e.FinishedAt = store.Now()
			e.DurationMs = time.Since(start).Milliseconds()
			e.Status = "ok"
			if err != nil {
				e.Status, e.Result = "error", err.Error()
			} else {
				if res != nil && res.IsError {
					e.Status = "error"
				}
				e.Result = mcpx.ResultSummary(res, 4000)
			}
			_, _ = r.db.Exec(`UPDATE tool_calls SET status=?, result=?, finished_at=?, duration_ms=? WHERE id=?`,
				e.Status, e.Result, e.FinishedAt, e.DurationMs, e.ID)
			r.bus.Publish(events.ToolFinished, e)
			return res, err
		}
	}
}

// List returns calls newest first. before is a started_at cursor (0 = newest).
func (r *Recorder) List(ctx context.Context, limit int, before int64, toolset string) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, toolset, tool, args, status, result, started_at, COALESCE(finished_at,0), COALESCE(duration_ms,0)
FROM tool_calls WHERE started_at < ? AND (? = '' OR toolset = ?)
ORDER BY started_at DESC LIMIT ?`, before, toolset, toolset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var args string
		if err := rows.Scan(&e.ID, &e.Toolset, &e.Tool, &args, &e.Status, &e.Result, &e.StartedAt, &e.FinishedAt, &e.DurationMs); err != nil {
			return nil, err
		}
		e.Args = json.RawMessage(args)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkInterrupted closes calls left "running" by a previous crash.
func (r *Recorder) MarkInterrupted() {
	_, _ = r.db.Exec(`UPDATE tool_calls SET status='error', result='interrupted (server stopped)' WHERE status='running'`)
}

// Prune deletes finished calls that started more than days ago. days <= 0
// keeps everything.
func (r *Recorder) Prune(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	res, err := r.db.ExecContext(ctx, `DELETE FROM tool_calls WHERE started_at < ? AND status != 'running'`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunRetention prunes at start and then hourly until ctx ends. days is read on
// every pass so a settings change applies without a restart.
func (r *Recorder) RunRetention(ctx context.Context, days func() int) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := r.Prune(ctx, days()); err == nil && n > 0 {
			log.Printf("activity: pruned %d calls older than %d days", n, days())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// redactArgs shrinks large string fields (file contents, post bodies stay
// readable up to a point) so the log stays light.
func redactArgs(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	for k, val := range m {
		if s, ok := val.(string); ok && len(s) > 2000 {
			m[k] = mcpx.Clip(s, 2000)
		}
	}
	out, _ := json.Marshal(m)
	return out
}
