// psql-manager 명령은 pg_service.conf와 .pgpass 항목을 서비스 단위로 함께 관리한다.
package main

import (
	"os"

	"github.com/Eung/psql-manager/internal/cli"
)

// version은 빌드할 때 -ldflags "-X main.version=..."으로 덮어쓴다.
var version = "dev"

func main() {
	os.Exit(cli.New(version).Run(os.Args[1:]))
}
