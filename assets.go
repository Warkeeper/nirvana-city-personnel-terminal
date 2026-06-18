package ncpt

import "embed"

// FrontendFS contains the Vue2/Element UI frontend served by the local app.
//
//go:embed frontend
var FrontendFS embed.FS
