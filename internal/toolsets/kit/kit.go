// Package kit is the dependency container handed to every toolset factory.
// Adding a tool means writing a package under internal/toolsets that exports
// a kit.Factory and listing it in internal/app/toolsets.go.
package kit

import (
	"database/sql"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/browser/session"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/jobs"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/posts"
	"github.com/Diwanshumidha/smart-control/internal/projects"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

type Deps struct {
	Version   string
	Config    *config.Config
	DB        *sql.DB
	Bus       *events.Bus
	Guard     *pathguard.Guard
	URLPolicy *urlpolicy.Policy
	Browser   *browser.Service
	Tabs      *session.Manager // interactive tabs that stay open across calls
	Jobs      *jobs.Manager
	Resources *resources.Service
	Posts     *posts.Repo
	Projects  *projects.Index

	// PublicURL is the tunneled base URL, or "" while no tunnel is up.
	PublicURL func() string
	// FileURL builds the public URL of a file served by a toolset's
	// PublicRoutes ("" while no tunnel is up).
	FileURL func(toolset, rel string) string
}

type Factory func(d *Deps) (mcpx.Toolset, error)
