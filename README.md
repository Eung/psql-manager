# psql-manager

`~/.pg_service.conf`와 `~/.pgpass`를 **서비스 이름 하나 기준으로 함께** 관리하고, 목록에서 골라 `psql`로 접속하게 해 주는 단일 바이너리 도구입니다.

- 서비스 이름을 외우지 않아도 설명(desc)과 태그로 찾아 접속합니다.
- 서비스 하나를 추가·수정·삭제하면 두 파일이 함께 바뀌어 서로 어긋나지 않습니다.
- 손으로 쓴 주석, 빈 줄, 순서는 건드리지 않습니다. 바뀐 줄 말고는 바이트 단위로 그대로입니다.
- 두 파일은 libpq 형식 그대로라서, 도구 없이도 `psql "service=이름"`으로 접속할 수 있습니다.

```text
$ psql-manager list
NAME            HOST            DBNAME    USER      PASSWORD  TAGS                      DESC
prod-settle-ro  db.example.com  settledb  readonly  managed   prod,readonly,settlement  운영 DB 읽기 전용 (정산)

$ psql-manager connect prod-settle-ro      # psql "service=prod-settle-ro"로 바로 접속
```

## 요구사항

- Linux 또는 macOS (Windows는 지원하지 않습니다)
- PostgreSQL 클라이언트(`psql`): `PATH`에 있거나 `PSQL_MANAGER_PSQL`로 지정
- 소스에서 빌드할 때만 Go 1.26 이상이 필요합니다. 빌드한 바이너리는 Go 없이 실행됩니다.

## 설치

```sh
git clone https://github.com/Eung/psql-manager.git
cd psql-manager
make build                      # dist/psql-manager
cp dist/psql-manager ~/bin/     # PATH에 들어 있는 아무 디렉터리
```

또는 `go install`로 설치합니다.

```sh
go install github.com/Eung/psql-manager/cmd/psql-manager@latest
```

다른 OS·아키텍처용 바이너리는 `make cross`로 만듭니다. 결과는 `dist/psql-manager-{linux,darwin}-{amd64,arm64}`이고, `CGO_ENABLED=0`인 정적 바이너리입니다.

## 빠른 시작

```sh
# 서비스 추가: 터미널에서는 비밀번호를 화면에 표시하지 않고 묻습니다(빈 값이면 비밀번호 없음).
psql-manager add prod-ro --host db.example.com --port 5432 --db app --user readonly \
    --desc "운영 DB 읽기 전용" --tags prod,readonly

# 터미널에서 인자 없이 실행하면 TUI에서 검색해 고르고 접속합니다.
psql-manager

# 접속 확인
psql-manager test prod-ro
```

## 파일 형식

두 파일 모두 libpq 형식을 그대로 따르고, 관리용 정보는 **주석으로만** 덧붙입니다. libpq는 주석을 읽지 않으므로 접속에는 영향이 없습니다.

### `~/.pg_service.conf`

섹션 헤더 **바로 위**에 붙은 `# desc:`, `# tags:` 주석을 메타데이터로 읽습니다. 그 밖의 주석은 읽지 않고 보존만 합니다.

```ini
# desc: 운영 DB 읽기 전용 (정산)
# tags: prod, readonly, settlement
# 담당: 김OO / 점검: 매주 화요일    <- 자유 주석, 보존만 함
[prod-settle-ro]
host=db.example.com
port=5432
dbname=settledb
user=readonly
sslmode=require
options=-c timezone=Asia/Seoul
```

- `password`는 이 파일에 쓰지 않습니다. 비밀번호는 `.pgpass`에만 둡니다.
- 서비스 이름은 `A-Z a-z 0-9 . _ -`만 쓸 수 있습니다.
- 줄바꿈 형식(LF, CRLF)과 파일 끝 개행 유무는 읽은 그대로 유지합니다.

### `~/.pgpass`

도구가 쓰는 줄 바로 위에는 `# psql-manager:<서비스명>` 표식을 붙입니다.

```text
# psql-manager:prod-settle-ro
db.example.com:5432:settledb:readonly:s3cr3t\:pw
```

- **표식이 없는 줄은 관리 대상이 아닙니다.** 도구는 그런 줄을 한 글자도 수정하거나 지우지 않습니다.
- 비밀번호 안의 `:`와 `\`는 `\:`, `\\`로 이스케이프해 씁니다.
- 새 줄은 그 줄을 가리게 될(먼저 매칭되는) 와일드카드(`*`) 줄보다 위에 넣고, 그런 줄이 없으면 파일 끝에 붙입니다.
- 서비스의 `host`가 비어 있거나 `/`로 시작하는 소켓 디렉터리이면(유닉스 도메인 소켓 접속), libpq처럼 `localhost`로 씁니다.
- 비밀번호를 저장하는 서비스에는 서비스 파일에 `port`, `dbname`, `user`가 있어야 합니다. libpq는 `.pgpass`에 빈 필드가 있는 줄을 매칭하지 않기 때문입니다.
- 서비스의 host, port, dbname, user가 바뀌면 표식이 붙은 줄도 함께 바뀝니다. 이름을 바꾸면 표식만 바뀝니다.

## 명령

| 명령 | 설명 |
| --- | --- |
| `psql-manager` | 터미널에서 실행하면 TUI를 띄웁니다. 고른 서비스로 접속합니다. |
| `add <name>` | 서비스를 추가하고 두 파일에 함께 씁니다. |
| `list` | 서비스 목록. 비밀번호는 상태만 보여 줍니다. |
| `show <name>` | 서비스 하나의 전체 설정과 각 값의 출처. 비밀번호는 가립니다. |
| `edit <name>` | 일부 값을 바꿉니다. `.pgpass`도 따라 바뀝니다. |
| `rm <name>` | 서비스 섹션과 소유한 `.pgpass` 줄을 함께 지웁니다. |
| `search <word>...` | 이름, desc, tags, host, dbname에서 검색합니다. |
| `connect [name]` | `psql "service=<name>"`으로 프로세스를 바꿉니다. |
| `test <name>` / `test --all` | `select 1`로 접속 가능 여부를 확인합니다. |
| `doctor [--fix]` | 두 파일의 불일치를 점검합니다. |
| `path` | 사용 중인 파일과 `psql` 경로를 보여 줍니다. |

명령별 옵션은 `psql-manager help <명령>`으로 볼 수 있습니다.

### add

```sh
# 옵션으로 값을 주면 비밀번호만 묻습니다.
psql-manager add prod-ro --host db.example.com --port 5432 --db app --user readonly \
    --desc "운영 DB 읽기 전용" --tags prod,readonly --option sslmode=require

# 이름만 주면 host, port, dbname, user, desc, tags를 차례로 묻습니다.
psql-manager add stg

# 스크립트에서는 표준 입력 첫 줄로 비밀번호를 넘깁니다.
printf '%s\n' "$PW" | psql-manager add ci --port 5432 --db app --user ci --password-stdin

# 비밀번호가 필요 없는 서비스(trust, 인증서 인증)
psql-manager add local-dev --db devdb --user me     # 터미널에서는 비밀번호 질문에 Enter
```

비밀번호를 명령줄 인자로 받는 옵션은 없습니다. `ps`와 셸 히스토리에 남기 때문입니다. 이미 있는 이름으로 `add`하면 거부합니다. 바꾸려면 `edit`을 쓰세요.

### list, search, show

```sh
psql-manager list                 # 최근 접속 순, 이력이 없는 서비스는 이름순
psql-manager list --tag prod
psql-manager list --json          # 스크립트용(비밀번호 값은 넣지 않음)

psql-manager search settle prod   # 모든 단어가 들어 있어야 일치, 이름 일치가 먼저
psql-manager search dev --json

psql-manager show prod-ro
psql-manager show prod-ro --reveal-password
```

`show`는 각 값이 어디서 왔는지 함께 보여 줍니다.

```text
name      dev-local
desc      -
tags      dev
host      -          (default: unix socket)
port      5432       (default)
dbname    devdb      (service file)
user      me         (service file)
password  ********   (unmanaged, /home/me/.pgpass line 2)
```

출처는 세 가지입니다. 순서는 libpq와 같습니다.

- `service file`: 서비스 파일에 적힌 값
- `env`: 키가 없을 때 쓰는 `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`
- `default`: 그래도 없을 때의 기본값. host는 소켓, port는 5432, dbname은 user와 같은 값, user는 OS 사용자명입니다.

**비밀번호 상태**

| 상태 | 뜻 |
| --- | --- |
| `managed` | 이 서비스의 표식이 붙은 `.pgpass` 줄이 쓰입니다. |
| `unmanaged` | 손으로 쓴(표식 없는) 줄이 쓰입니다. 도구는 그 줄을 고치지 않습니다. |
| `shared` | 같은 host:port:dbname:user를 쓰는 다른 서비스의 줄이 쓰입니다. |
| `none` | 맞는 줄이 없습니다. 비밀번호를 묻거나, 비밀번호 없는 인증으로 접속합니다. |

`list`에서 상태 뒤에 `*`가 붙은 행은 환경변수나 기본값까지 써서 판정한 것입니다. 다른 환경에서는 결과가 다를 수 있습니다.

### edit

```sh
psql-manager edit prod-ro --port 6432 --option sslmode=require
psql-manager edit prod-ro --port ""                # 빈 값이면 그 키를 지웁니다
psql-manager edit prod-ro --option sslmode=        # 다른 파라미터도 같은 방식
psql-manager edit prod-ro --desc "새 설명" --tags prod,ro
psql-manager edit prod-ro --rename settle-ro       # .pgpass는 표식만 바뀝니다
psql-manager edit prod-ro --change-password        # 터미널에서 새 비밀번호를 묻습니다
printf '%s\n' "$PW" | psql-manager edit prod-ro --password-stdin
psql-manager edit prod-ro --remove-password        # 소유한 .pgpass 줄을 지웁니다
```

### rm

```sh
psql-manager rm prod-ro          # 터미널에서 확인을 받습니다
psql-manager rm -y prod-ro       # 확인 생략(스크립트에서는 필수)
```

섹션 바로 위에 붙은 주석(desc, tags 등)은 함께 지우고, 빈 줄로 떨어진 주석은 남깁니다.

### connect, test

```sh
psql-manager connect prod-ro
psql-manager connect prod-ro -- -c 'select now()'   # -- 뒤는 psql에 그대로 전달
psql-manager connect                                # 이름이 없으면 TUI에서 고릅니다

psql-manager test prod-ro
psql-manager test --all                             # 실패가 있으면 종료 코드 5
```

- `connect`는 자식 프로세스를 띄우지 않고, 현재 프로세스를 `psql "service=<name>"`으로 바꿉니다. `PGSERVICE` 환경변수는 건드리지 않습니다. 접속 직전에 이력을 남기고, 이력을 쓰지 못해도 접속은 진행합니다.
- `test`는 `psql "service=<name> connect_timeout=3" -w -X -Atc "select 1"`을 실행합니다. `-w`가 있어 비밀번호 질문에 걸려 멈추지 않습니다. `-X`는 개인 `.psqlrc`가 결과에 섞이지 않게 합니다.

### doctor

```sh
psql-manager doctor          # 점검만 합니다. 고치지 못한 error가 남으면 종료 코드 4
psql-manager doctor --fix    # 안전하게 고칠 수 있는 것만 고칩니다
```

| 점검 항목 | 심각도 | `--fix` |
| --- | --- | --- |
| 서비스 파일·`.pgpass` 형식 오류 | error | 직접 고치기 |
| 같은 이름의 섹션 중복 | error | 직접 고치기 |
| `.pgpass` 권한이 0600이 아님 | error | 0600으로 변경 |
| 서비스는 없는데 표식이 남은 줄(고아 항목) | error | 삭제 |
| 표식 아래에 항목 줄이 없음 | error | 표식 삭제 |
| 관리 줄의 키가 서비스 값과 다름 | error | 서비스 값으로 다시 맞춤 |
| 관리 줄보다 앞 줄이 먼저 매칭됨 | error | 직접 고치기 |
| 손으로 쓴 줄이 와일드카드 등 앞 줄에 가려 쓰이지 않음 | warning | 직접 고치기 |
| 서비스에 맞는 `.pgpass` 줄이 없음 | info | — |

형식 오류가 있으면 `--fix`는 권한만 고치고, 파일 내용을 바꾸는 수정은 건너뜁니다.

### path

```text
$ psql-manager path
service file: /home/me/.pg_service.conf (default)
pgpass:       /home/me/.pgpass (default)
psql:         /usr/bin/psql (PATH)
```

## TUI

터미널에서 `psql-manager`만 실행하면 왼쪽에 서비스 목록, 오른쪽에 상세 패널이 뜹니다. 바로 입력하면 검색됩니다.

| 키 | 동작 |
| --- | --- |
| `↑` `↓`, `Ctrl+P` `Ctrl+N` | 목록 이동 |
| 문자 입력 | 검색(검색창이 기본 포커스) |
| `Enter` | 선택한 서비스로 접속(TUI를 닫고 `psql`로 교체) |
| `Ctrl+E` / `Ctrl+A` | 편집 폼 / 추가 폼 |
| `Ctrl+D` | 삭제(확인 필수) |
| `Ctrl+T` | 접속 테스트. 목록에 ●(성공) / ○(실패) 표시 |
| `Ctrl+R` | 상세 패널의 비밀번호 표시 켜기·끄기 |
| `Tab` | 태그 필터 바꾸기 |
| `Esc`, `Ctrl+C` | 폼·필터·검색어 취소, 최상위에서는 종료 |

- 폼의 비밀번호 칸은 항상 가립니다. 편집 폼에서 비워 두면 기존 비밀번호를 유지합니다.
- 폼의 `options`는 libpq의 `options` 파라미터입니다. 폼에 없는 파라미터(`sslmode` 등)는 그대로 보존되고, `edit --option`으로 바꿉니다.
- 터미널 폭이 80보다 좁으면 상세 패널을 접습니다.

## 환경변수

| 변수 | 뜻 | 기본값 |
| --- | --- | --- |
| `PGSERVICEFILE` | 서비스 파일 경로 | `~/.pg_service.conf` |
| `PGPASSFILE` | 비밀번호 파일 경로 | `~/.pgpass` |
| `PSQL_MANAGER_PSQL` | 실행할 `psql` 경로 | `PATH`의 `psql` |
| `XDG_STATE_HOME` | 접속 이력 위치(`$XDG_STATE_HOME/psql-manager/history`) | `~/.local/state` |

시스템 전체 서비스 파일(`pg_config --sysconfdir`)은 읽지도 쓰지도 않습니다.

## 종료 코드

| 코드 | 뜻 |
| --- | --- |
| 0 | 성공 |
| 2 | 사용 오류(잘못된 옵션, 이름, 값, 중복 이름 등) |
| 3 | 대상 서비스 없음 |
| 4 | 파일·권한 오류, 잠금 충돌, 두 파일 상태 문제, `psql` 없음 |
| 5 | 접속 실패(`test`) |

## 안전장치

- **원자적 쓰기**: 같은 디렉터리에 0600 임시 파일을 만들어 쓰고 `fsync`한 뒤 `rename`합니다. 두 파일을 함께 바꿀 때는 임시 파일을 둘 다 쓴 다음 차례로 교체합니다. 두 번째 교체가 실패하면 첫 번째 파일을 되돌립니다.
- **백업**: 바꾸기 전 원본을 `<파일>.bak`(0600)으로 복사해 직전 한 세대만 남깁니다.
- **잠금**: `~/.psql-manager.lock`에 `flock`을 겁니다. 다른 프로세스가 쓰는 중이면 기다리지 않고 바로 오류로 끝냅니다.
- **권한**: 두 파일은 0600으로 만들고, `.pgpass` 권한이 0600이 아니면 경고한 뒤 고칩니다.
- **심볼릭 링크**: 링크를 따라가 실제 파일에 씁니다. 링크 자체는 바꾸지 않습니다.
- **저장 실패**: 아래 경우에는 두 파일 모두 건드리지 않고 저장을 거부합니다.
  - 두 파일 중 하나에 libpq가 해석하지 못할 줄이 있을 때. 줄 번호를 알려 줍니다.
  - 다른 서비스가 같은 host:port:dbname:user로 이미 비밀번호를 저장했을 때
  - 표식 없는 기존 줄이 먼저 매칭되어 새 비밀번호가 쓰이지 않을 때
  - 서비스는 없는데 같은 이름의 관리 줄이 남아 있는 상태에서 비밀번호 없이 `add`할 때
- **비밀번호 노출 방지**: 오류 메시지, 경고, `--json`, `list`, `show`에는 비밀번호 값을 넣지 않습니다. `--reveal-password`와 TUI의 `Ctrl+R`만 예외입니다.

> **dotfiles 저장소를 쓴다면**: 홈의 `.pgpass`가 dotfiles 저장소 안의 파일로 연결된 링크라면, 백업 `.pgpass.bak`도 그 저장소 안에 생깁니다. 백업에는 비밀번호가 들어 있으므로, 저장소의 `.gitignore`에 `*.bak`을 넣어 두세요.

## 개발

```sh
make build        # dist/psql-manager (버전은 git describe로 주입)
make check        # gofmt 검사, go vet, go test
make cover        # 커버리지
make integration  # Docker postgres에 실제로 접속하는 테스트 포함
make cross        # linux, darwin × amd64, arm64
make clean
```

- 테스트는 실제 `~/.pgpass`와 `~/.pg_service.conf`를 읽거나 쓰지 않습니다. 모두 `t.TempDir()`과 `PGSERVICEFILE`, `PGPASSFILE`, `HOME`, `XDG_STATE_HOME`으로 격리합니다.
- CLI 골든 테스트는 `testdata/cli/<케이스>/`에 있습니다. 결과가 바뀌면 `go test ./internal/cli -run TestGolden -update`로 다시 기록한 뒤 diff를 검토합니다. 골든 파일에는 비밀번호 대신 `{{pw:이름}}` 토큰만 둡니다.
- `connect`, `test`는 가짜 `psql` 스크립트로 실제 바이너리를 실행해 시험합니다(`cmd/psql-manager/main_test.go`).

코드 구조와 설계는 [SPEC.md](SPEC.md)를 참고하세요.
