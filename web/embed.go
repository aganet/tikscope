// Package web holds the dashboard, served by the API from the binary.
package web

import "embed"

//go:embed index.html login.html app.css js
var FS embed.FS
