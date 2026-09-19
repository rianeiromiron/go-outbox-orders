// Package migrations embebe las migraciones SQL para que el binario (y el
// job de migración de Compose/k8s) no dependa de archivos externos.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
