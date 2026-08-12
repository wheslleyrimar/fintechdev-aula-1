// Package migrations carrega o schema do ledger embutido no binário.
// Um deploy, um schema: o monólito modular sobe o banco na versão que ele espera.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
