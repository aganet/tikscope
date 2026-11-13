// Package web holds the dashboard, served by the API from the binary.
package web

import "embed"

//go:embed index.html login.html app.css logo.svg js
var FS embed.FS
