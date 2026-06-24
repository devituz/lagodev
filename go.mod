module github.com/devituz/lagodev

go 1.25.0

// Build with a patched toolchain: go1.26.0–1.26.3 ship stdlib vulnerabilities
// (net/mail, net/textproto, crypto/x509, crypto/tls, net/http, os, net/url)
// that lagodev's mail/web/db paths reach. 1.26.4 fixes all of them.
toolchain go1.26.4

require (
	github.com/brianvoe/gofakeit/v7 v7.15.0
	github.com/fatih/color v1.19.0
	github.com/go-sql-driver/mysql v1.10.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/jackc/pgx/v5 v5.10.0
	github.com/joho/godotenv v1.5.1
	github.com/manifoldco/promptui v0.9.0
	github.com/mattn/go-sqlite3 v1.14.47
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.53.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/chzyer/readline v1.5.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
