package app

import (
	"github.com/Diwanshumidha/smart-control/internal/toolsets/brag"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/browserkit"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/kit"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/linkedin"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/media"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/project"
)

// Toolsets is the list of installed toolsets. To add tools, write a package
// under internal/toolsets exporting a kit.Factory and append it here.
var Toolsets = []kit.Factory{
	project.New,
	browserkit.New,
	media.New, // before brag/linkedin: registers the project:// source
	linkedin.New,
	brag.New,
}
