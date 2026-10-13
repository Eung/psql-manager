# psql-manager 작업 명세서 (Go)

Oct 12, 2026 · @응진

## 1. 개요와 목표

**psql-manager**는 `~/.pg_service.conf`와 `~/.pgpass`를 하나의 서비스 이름 기준으로 함께 관리하고, 목록에서 골라 `psql`로 접속하게 해 주는 Go 단일 바이너리 도구다.

**풀려는 문제**

- 서비스가 많아지면 이름을 기억하기 어렵다. 이름 대신 설명과 태그를 보고 고를 수 있어야 한다.
- 두 파일을 따로 편집하면 서로 어긋난다. 서비스 단위로 두 파일을 동시에 갱신해야 한다.
- 손으로 쓴 주석과 항목 순서를 도구가 망가뜨리면 안 된다.

**목표**

1. 서비스 추가, 수정, 삭제, 조회를 CLI로 제공한다.
2. 이름 없이 실행하면 TUI 선택 화면에서 검색하고 접속한다.
3. 도구 없이도 두 파일이 libpq와 사람이 읽기에 그대로 유효해야 한다.

**비목표**

- 비밀번호를 암호화해 저장하거나 키체인과 연동하지 않는다. 저장 방식은 `.pgpass` 평문(0600)을 그대로 따른다.
- `psql`을 대체하지 않는다. 접속은 항상 `psql`에 위임한다.
- Windows 지원은 1차 범위에서 제외한다. Linux와 macOS만 대상으로 한다.

## 2. 파일 형식 명세

두 파일의 형식을 libpq 규칙 그대로 지키고, 관리용 메타데이터는 **주석으로만** 추가한다. libpq가 읽는 데 영향이 없어야 한다.

### 2.1 \~/.pg\_service.conf

- 위치 우선순위: `PGSERVICEFILE` 환경변수, 그다음 `~/.pg_service.conf`.
- INI 형식이며 `[서비스명]` 섹션 아래에 `키=값`을 쓴다. 키는 libpq 접속 파라미터(`host`, `port`, `dbname`, `user`, `sslmode`, `options`, `connect_timeout` 등)다.
- `password`는 이 파일에 쓰지 않는다. 비밀번호는 `.pgpass`에만 둔다.
- `#` 또는 `;`로 시작하는 줄은 주석이다. 값 뒤 인라인 주석은 값의 일부가 되므로 도구가 쓰지도, 해석하지도 않는다.

### 2.2 메타 주석 규칙

섹션 헤더 **바로 위** 연속된 주석 줄에서 아래 키를 읽는다. 알 수 없는 주석은 그대로 보존한다.

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

| 키 | 의미 | 형식 |
| --- | --- | --- |
| `# desc:` | 한 줄 설명 | 자유 텍스트 |
| `# tags:` | 분류 태그 | 쉼표 구분, 소문자 권장 |

### 2.3 \~/.pgpass

- 위치 우선순위: `PGPASSFILE` 환경변수, 그다음 `~/.pgpass`. 권한은 반드시 `0600`.
- 한 줄 형식은 `hostname:port:database:username:password`다.
- 값 안의 `:`와 `\`는 `\:`와 `\\`로 이스케이프한다. 읽을 때는 반대로 해석한다.
- `#`로 시작하는 줄은 주석이다. 인라인 주석은 비밀번호의 일부가 되므로 쓰지 않는다.
- 여러 줄이 일치하면 **위에서 처음 일치하는 줄**이 쓰인다. 와일드카드(`*`) 줄은 구체적인 줄보다 아래에 둔다.
- 서비스 `host`가 비어 있거나 `/`로 시작하는 소켓 디렉터리 경로이면(유닉스 도메인 소켓 접속) pgpass에서는 `localhost`로 매칭된다. 도구도 pgpass 줄의 hostname을 `localhost`로 쓰고, 서비스 파일의 `host` 값은 그대로 둔다.
- libpq는 빈 `port`, `dbname`, `user`를 기본값으로 바꿔 매칭하므로 빈 필드가 있는 줄은 절대 쓰이지 않는다. 그래서 비밀번호를 저장하는 서비스는 `port`, `dbname`, `user`를 반드시 입력해야 한다.

### 2.4 서비스와 pgpass 줄의 연결

별도 키 없이 서비스의 `host`, `port`, `dbname`, `user` 네 값으로 pgpass 줄을 찾는다. 도구가 쓰는 pgpass 줄 바로 위에 `# psql-manager:<서비스명>` 주석을 한 줄 붙여 소유 관계를 표시한다.

```
# psql-manager:prod-settle-ro
db.example.com:5432:settledb:readonly:s3cr3t\:pw
```

- 이 표식이 없는 pgpass 줄은 **관리 대상이 아니다**. 도구는 수정, 삭제하지 않고 `list`에서 "unmanaged"로만 표시한다.
- 서비스의 네 값이 바뀌면 같은 표식의 pgpass 줄도 새 값으로 갱신한다.

## 3. 프로젝트 구조와 기술 스택

핵심 로직은 `internal/` 아래 라이브러리 패키지로 두고, CLI와 TUI는 이를 호출하는 얇은 계층으로 만든다. TUI가 나중에 붙어도 코어를 고치지 않게 하기 위함이다.

```
psql-manager/
├── cmd/psql-manager/main.go          # 진입점, 서브커맨드 라우팅
├── internal/
│   ├── svcfile/               # pg_service.conf 파서/라이터 (주석 보존)
│   ├── pgpass/                # .pgpass 파서/라이터, 이스케이프, 매칭
│   ├── store/                 # 두 파일을 묶는 서비스 단위 CRUD (핵심 도메인)
│   ├── fsutil/                # 원자적 쓰기, 0600, .bak 백업, 파일 락
│   ├── history/               # 접속 이력 (~/.local/state/psql-manager/history)
│   ├── connect/               # psql 탐색, syscall.Exec, 접속 테스트
│   ├── cli/                   # 서브커맨드 구현, 출력 포맷
│   └── tui/                   # bubbletea 모델, 뷰, 키맵
├── testdata/                  # 골든 파일용 샘플 service/pgpass
├── Makefile                   # build, test, check, cross 타깃
├── go.mod
└── README.md
```

| 패키지 | 책임 | 의존해도 되는 것 |
| --- | --- | --- |
| `svcfile` | 파일을 줄 단위 구조로 읽고 되쓴다. 섹션 단위 추가, 수정, 삭제를 제공한다. | 표준 라이브러리만 |
| `pgpass` | 줄 파싱, 이스케이프, 매칭 규칙을 구현한다. | 표준 라이브러리만 |
| `store` | 서비스 하나를 두 파일에 걸쳐 일관되게 다룬다. | `svcfile`, `pgpass`, `fsutil` |
| `connect` | `psql` 경로 탐색과 프로세스 교체, `select 1` 테스트를 담당한다. | 표준 라이브러리만 |
| `cli`, `tui` | 입출력만 담당하고 비즈니스 로직은 `store`에 위임한다. | `store`, `history`, `connect` |

**기술 스택**

- Go 1.22 이상, 모듈명은 `github.com/Eung/psql-manager` (`저장소: https://github.com/Eung/psql-manager). 바이너리 이름도 psql-manager다.`.
- CLI 파싱은 표준 `flag` + 서브커맨드 직접 라우팅을 기본으로 한다. 서브커맨드가 늘면 `spf13/cobra`로 교체할 수 있게 `cli` 패키지 안에 가둔다.
- TUI는 `charmbracelet/bubbletea`, `bubbles`(list, textinput), `lipgloss`를 사용한다.
- 퍼지 검색은 별도 라이브러리 없이 부분 문자열 + 단어 단위 매칭으로 직접 구현한다. 필요하면 `sahilm/fuzzy`를 검토한다.
- 비밀번호 프롬프트는 `golang.org/x/term`의 `ReadPassword`를 쓴다.
- `svcfile`, `pgpass`, `connect`는 외부 의존성 없이 표준 라이브러리만 쓴다.
- 빌드와 검사는 저장소 루트의 `Makefile`로 통일한다: `make build`(`dist/psql-manager`), `make check`(gofmt 검사, `go vet`, `go test`), `make cover`, `make integration`, `make cross`(linux, darwin의 amd64, arm64를 `CGO_ENABLED=0`으로 빌드), `make clean`. 버전은 `-ldflags "-X main.version=..."`로 주입하며, `cmd/psql-manager/main.go`에 `var version = "dev"`를 둔다. `dist/`와 `coverage.out`은 `.gitignore`에 넣는다.

## 4. CLI 명령 명세

인자 없이 `psql-manager`만 실행하면 TUI(6절)를 띄운다. TUI 구현 전에는 목록 선택 후 접속하는 간단한 번호 선택 화면으로 대체한다.

| 명령 | 동작 | 주요 옵션 |
| --- | --- | --- |
| `psql-manager add <name>` | 서비스를 추가하고 두 파일에 함께 쓴다. 값은 옵션 또는 대화형 프롬프트로 받는다. | `--host --port --db --user --desc --tags --option k=v`, 비밀번호는 항상 프롬프트 또는 `--password-stdin` |
| `psql-manager list` | 서비스 표(이름, host, dbname, user, desc, tags)를 출력한다. 비밀번호 유무만 표시한다. | `--tag <t>`, `--json` |
| `psql-manager show <name>` | 서비스 한 개의 전체 설정을 출력한다. 비밀번호는 마스킹한다. | `--reveal-password` |
| `psql-manager edit <name>` | 일부 값을 바꾼다. 네 값이 바뀌면 pgpass 줄도 함께 갱신한다. | `add`와 같은 옵션, `--rename <new>` |
| `psql-manager rm <name>` | 서비스 섹션과 소유 pgpass 줄을 함께 지운다. | `-y` 확인 생략 |
| `psql-manager search <word>` | 이름, desc, tags, host, dbname에서 검색한다. | `--json` |
| `psql-manager test <name>` | `connect_timeout=3`으로 `select 1`을 실행해 접속 가능 여부를 확인한다. | `--all` |
| `psql-manager connect [name]` | `psql "service=<name>"`으로 프로세스를 교체한다. 이름이 없으면 선택 화면을 띄운다. | `-- <psql 인자...>` 그대로 전달 |
| `psql-manager doctor` | 두 파일의 불일치를 점검한다. | `--fix` |
| `psql-manager path` | 사용 중인 파일 경로와 `psql` 경로를 출력한다. | 없음 |

**공통 규칙**

- 종료 코드는 성공 `0`, 사용 오류 `2`, 대상 서비스 없음 `3`, 파일/권한 오류 `4`, 접속 실패 `5`로 한다.
- 오류는 stderr, 결과는 stdout으로 출력한다. `--json`은 스크립트에서 쓸 수 있게 안정적인 필드명을 쓴다.
- 비밀번호를 명령줄 인자로 받는 옵션은 만들지 않는다. `ps`와 셸 히스토리에 남기 때문이다.
- 서비스명은 `[A-Za-z0-9._-]+`만 허용한다. 대괄호, 공백, 개행은 거부한다.
- 이미 있는 이름으로 `add`하면 거부한다. 덮어쓰려면 `edit`을 쓴다.

**`doctor`가 점검하는 항목**

- 서비스는 있는데 대응하는 pgpass 줄이 없음 (비밀번호 미설정)
- `# psql-manager:<name>` 표식은 있는데 서비스가 없는 pgpass 줄 (고아 항목)
- 같은 서비스명 섹션이 중복됨
- `.pgpass` 권한이 `0600`이 아님
- 와일드카드 줄이 구체적인 줄보다 위에 있어 가려지는 경우

## 5. 핵심 로직 요구사항

### 5.1 주석 보존 서비스 파일 파서 (`svcfile`)

- 파일을 파싱 후 재직렬화하지 않고, **줄 단위 구조**로 들고 있다가 바뀐 부분만 고쳐 쓴다. 수정하지 않은 줄은 공백, 주석, 순서까지 바이트 단위로 같아야 한다.
- 모델: 섹션 하나는 `{헤더 앞 주석 줄들, 이름, 키=값 줄들, 뒤따르는 빈 줄}`이다. 첫 섹션 앞의 주석은 파일 머리말로 따로 보존한다.
- 키=값 수정은 해당 줄만 교체하고, 없는 키는 섹션의 마지막 키 줄 뒤에 추가한다.
- 섹션 삭제는 헤더 앞 주석 줄까지 함께 지운다. 단, 빈 줄로 분리된 앞 주석은 남긴다.
- 새 섹션은 파일 끝에 빈 줄 하나를 두고 추가한다. `# desc:`, `# tags:` 순서로 쓴다.
- 줄바꿈 형식(LF 또는 CRLF)과 파일 끝 개행 유무를 읽은 그대로 유지한다.
- 파싱 실패는 줄 번호와 함께 오류로 돌려주고, 이해하지 못한 줄도 원문 그대로 보존한다.

### 5.2 pgpass 처리 (`pgpass`)

- 파서는 `\:`와 `\\` 이스케이프를 해석하고, 라이터는 값 안의 `:`와 `\`를 이스케이프해 쓴다.
- 비밀번호 끝의 개행은 값에 포함하지 않는다. 비밀번호에 개행이 들어 있으면 거부한다.
- 도구가 관리하는 줄은 `# psql-manager:<name>` 표식 + 데이터 줄 두 줄 한 쌍으로 다룬다. 표식 없는 줄은 한 글자도 건드리지 않는다.
- 새 줄은 그 줄과 **일치해서 가리게 될** 첫 와일드카드 줄(바로 위에 붙은 주석 포함)보다 **위**에 삽입한다. 일치하는 와일드카드 줄이 없으면 파일 끝에 추가한다.
- 이름 변경(`--rename`)은 표식 주석만 바꾼다.

### 5.3 서비스 단위 CRUD (`store`)

- `Add`, `Update`, `Remove`, `Get`, `List`, `Search` API를 제공한다. 두 파일을 함께 바꾸는 연산은 **한 번의 트랜잭션처럼** 동작해야 한다.
- 순서: 두 파일을 읽는다, 메모리에서 변경을 적용한다, 임시 파일 두 개를 먼저 쓴다, 둘 다 성공하면 차례로 `rename`한다. 임시 파일 단계에서 실패하면 원본은 그대로다.
- `rename`이 첫 번째는 성공하고 두 번째가 실패하면 첫 번째를 `.bak`에서 복구하고 오류를 돌려준다.
- `Search`는 이름, desc, tags, host, dbname을 대상으로 대소문자 무시, 공백으로 나눈 단어가 모두 포함되면 일치로 본다. 이름 일치를 가장 높게 점수를 준다.

### 5.4 안전한 쓰기 (`fsutil`)

- 같은 디렉터리에 임시 파일을 만들어 쓰고 `fsync` 후 `rename`한다. 임시 파일 권한은 처음부터 `0600`이다.
- 수정 전 원본을 `<파일>.bak`으로 복사한다. 직전 한 세대만 유지한다.
- 동시 실행을 막기 위해 `~/.psql-manager.lock`에 `flock`을 건다. 잠금을 못 얻으면 대기 없이 오류로 끝낸다.
- `.pgpass` 권한이 `0600`이 아니면 쓰기 전에 경고하고 `0600`으로 고친다.
- 심볼릭 링크로 연결된 파일(dotfiles 관리 등)은 링크를 따라가 실제 파일에 쓴다. 링크 자체를 교체하지 않는다.
- 파일이 없으면 새로 만든다. `~/.pgpass`는 `0600`, 서비스 파일은 `0600`으로 만든다.

### 5.5 접속과 테스트 (`connect`)

- `exec.LookPath("psql")`로 찾고, 없으면 설치 안내와 함께 종료 코드 `4`로 끝낸다. `PSQL_MANAGER_PSQL` 환경변수로 경로를 지정할 수 있다.
- 접속은 `syscall.Exec`로 `psql`로 프로세스를 교체한다. 환경변수 `PGSERVICE`는 건드리지 않고 `service=<name>` 접속 문자열을 인자로 넘긴다.
- `test`는 `psql "service=<name> connect_timeout=3" -Atc "select 1"`을 자식 프로세스로 실행하고, 출력과 종료 코드로 성공 여부를 판단한다. 비밀번호가 프롬프트로 떨어지지 않도록 `PGCONNECT_TIMEOUT`과 `-w`(`--no-password`)를 쓴다.

### 5.6 접속 이력 (`history`)

- 위치는 `$XDG_STATE_HOME/psql-manager/history`이고, 없으면 `~/.local/state/psql-manager/history`다.
- 한 줄에 `<unix시각>\t<서비스명>` 형식으로 `connect` 직전에 추가한다. 최근 1000줄만 유지한다.
- 목록 정렬은 최근 접속 순을 기본으로 하고, 접속 이력이 없는 서비스는 이름순으로 뒤에 둔다.
- 이력 파일을 쓰지 못해도 접속은 막지 않는다.

## 6. TUI 명세

`psql-manager`를 인자 없이 실행하면 왼쪽에 서비스 목록, 오른쪽에 선택한 서비스의 상세를 보여주는 두 패널 화면이 뜬다. 상단 검색창에 입력하면 즉시 목록이 걸러진다.

```
┌ psql-manager ──────────────────────────────────────┐
│ 검색: settle_                                      │
├────────────────────────┬───────────────────────────┤
│ ▶ prod-settle-ro       │ host    db.example.com    │
│   prod-settle-rw       │ port    5432              │
│   stg-settle           │ dbname  settledb          │
│   dev-local            │ user    readonly          │
│                        │ options timezone=Asia/... │
│                        │ tags    prod, readonly    │
│                        │ desc    정산 DB 읽기 전용 │
│                        │ pass    ●●●● (설정됨)     │
├────────────────────────┴───────────────────────────┤
│ Enter 접속  ^E 편집  ^A 추가  ^D 삭제  ^T 테스트  Esc 종료 │
└────────────────────────────────────────────────────┘
```

**키 바인딩**

| 키 | 동작 |
| --- | --- |
| `↑/↓`, `Ctrl+P/N` | 목록 이동 |
| 문자 입력 | 검색어 입력 (검색창이 기본 포커스) |
| `Enter` | 선택한 서비스로 접속 (TUI 종료 후 `psql`로 프로세스 교체) |
| `Ctrl+E` | 선택한 서비스 편집 폼 열기 |
| `Ctrl+A` | 서비스 추가 폼 열기 |
| `Ctrl+D` | 삭제 (확인 프롬프트 필수) |
| `Ctrl+T` | 선택한 서비스 접속 테스트, 목록에 ●/○로 표시 |
| `Ctrl+R` | 상세 패널의 비밀번호 표시 토글 |
| `Tab` | 태그 필터 전환 |
| `Esc`, `Ctrl+C` | 폼/필터 취소, 최상위에서는 종료 |

**동작 요구사항**

- 목록은 최근 접속 순으로 시작하고, 검색어가 있으면 점수순으로 정렬한다.
- 접속 테스트는 비동기(`tea.Cmd`)로 실행해 화면이 멈추지 않아야 한다. 타임아웃은 3초다.
- 접속 시 `tea.Quit`로 터미널을 정상 복원한 뒤 `connect.Exec`를 호출한다. 자식 프로세스로 띄우지 않는다.
- 추가, 편집 폼의 입력 필드는 이름, host, port, dbname, user, password, options, desc, tags이다. 비밀번호 필드는 항상 마스킹한다.
- 편집 폼에서 비밀번호를 비워 두면 기존 값을 유지한다.
- 상세 패널의 비밀번호는 기본으로 마스킹하고, 표시 토글을 끄면 즉시 다시 가린다.
- 터미널 크기가 작으면(폭 80 미만) 상세 패널을 접고 목록만 보여준다.
- 서비스 파일이 비어 있거나 없으면 빈 상태 화면에서 `Ctrl+A`로 첫 서비스를 추가하도록 안내한다.

## 7. 테스트 전략과 완료 기준

실제 사용자 홈 디렉터리를 건드리지 않도록 모든 테스트는 `t.TempDir()`과 `PGSERVICEFILE`, `PGPASSFILE` 환경변수로 격리한다.

**단위 테스트**

| 대상 | 필수 케이스 |
| --- | --- |
| `svcfile` | 주석, 빈 줄, 순서가 있는 샘플 파일을 읽고 변경 없이 쓰면 **바이트 동일**. 섹션 추가, 키 수정, 키 추가, 섹션 삭제 후 다른 부분 불변. CRLF 파일, 끝 개행 없는 파일, 첫 섹션 앞 머리말 주석 |
| `pgpass` | `\:`와 `\\` 이스케이프 왕복, 비밀번호에 `:` `\` `#`가 들어간 경우, 표식 없는 줄 불변, 와일드카드 줄 앞 삽입, 빈 host(`localhost`) 처리 |
| `store` | 추가 후 두 파일 모두 반영, 네 값 변경 시 pgpass 동기화, 이름 변경, 삭제 시 소유 줄만 제거, 중복 이름 거부, 두 번째 `rename` 실패 시 복구 |
| `fsutil` | 권한 `0600`, 임시 파일 정리, 심볼릭 링크 대상 쓰기, 잠금 충돌 |
| `history` | 1000줄 제한, 쓰기 실패 시 무시 |

**통합 테스트**

- `testdata/` 골든 파일과 비교하는 CLI 테스트: `add`, `edit`, `rm` 결과가 기대 파일과 정확히 같은지 확인한다.
- 가짜 `psql` 셸 스크립트를 `PSQL_MANAGER_PSQL`로 지정해 `connect`, `test`가 올바른 인자를 넘기는지 확인한다. 실제 PostgreSQL은 필요 없다.
- 선택 테스트로 Docker의 `postgres` 컨테이너에 실제 접속하는 테스트를 빌드 태그(`integration`)로 분리한다.

**TUI 테스트**

- bubbletea 모델의 `Update`에 키 메시지를 직접 넣어 상태 전이를 검증한다(검색 필터, 폼 열기/닫기, 삭제 확인).
- 화면 렌더링은 `View()` 문자열의 핵심 부분 포함 여부만 확인하고, 전체 스냅샷 비교는 하지 않는다.

**완료 기준**

- [ ] `make check`(gofmt 검사, `go vet`, `go test`)가 통과한다. `svcfile`, `pgpass`, `store`의 라인 커버리지는 85% 이상이다.
- [ ] 주석이 가득한 실제 형태의 서비스 파일에 `add`, `edit`, `rm`을 수행해도 다른 줄이 바이트 단위로 보존된다.
- [ ] `psql-manager add` 한 번으로 만든 서비스를 `psql "service=<name>"`으로 비밀번호 입력 없이 접속할 수 있다.
- [ ] `psql-manager doctor`가 4절의 불일치 항목을 모두 탐지한다.
- [ ] TUI에서 검색, 접속, 추가, 편집, 삭제, 테스트가 동작한다.
- [ ] `GOOS=linux`, `GOOS=darwin` make cross로 linux, darwin(amd64, arm64) 단일 바이너리가 dist/에 빌드된다.
- [ ] README에 설치법, 파일 형식, 모든 명령 예시가 있다.

## 8. 마일스톤과 Claude Code 작업 지침

아래 순서로 단계마다 테스트까지 통과시킨 뒤 다음으로 넘어간다. 각 단계는 독립적으로 커밋 가능한 상태여야 한다.

1. **M1. 파서와 라이터**: `svcfile`, `pgpass`, `fsutil`을 구현하고 단위 테스트를 통과시킨다. 바이트 동일 보존 테스트가 가장 먼저다.
2. **M2. 서비스 단위 CRUD**: `store`를 구현하고 두 파일의 동기화, 복구, 소유 표식 규칙을 테스트한다.
3. **M3. CLI**: `add`, `list`, `show`, `edit`, `rm`, `search`, `path`를 구현한다. 골든 파일 통합 테스트를 붙인다.
4. **M4. 접속**: `connect`, `test`, `history`, `doctor`를 구현한다. 가짜 `psql`로 테스트한다. 이 시점에 번호 선택식 `psql-manager connect`가 동작한다.
5. **M5. TUI**: 목록, 검색, 상세 패널, 접속을 먼저 만들고, 이어서 추가, 편집, 삭제 폼과 접속 테스트 표시를 붙인다.
6. **M6. 마무리**: README, 크로스 컴파일, `--help` 문구, 오류 메시지를 정리한다.

**Claude Code에 주는 작업 규칙**

- 시작하기 전에 이 문서를 `SPEC.md`로 저장하고, 저장소 루트의 `CLAUDE.md`에 "작업 전 `SPEC.md`를 읽고 마일스톤 순서를 따를 것"과 아래 규칙을 적는다.
- 한 번에 한 마일스톤만 진행하고, 끝나면 무엇을 만들었고 무엇이 남았는지 요약한다. 다음 마일스톤으로 넘어가기 전에 확인을 받는다.
- 명세와 다르게 구현해야 할 이유가 생기면 임의로 바꾸지 말고 이유와 대안을 먼저 제시한다.
- 사용자 홈의 실제 `~/.pgpass`, `~/.pg_service.conf`를 테스트나 개발 중에 읽거나 쓰지 않는다. 항상 임시 경로와 환경변수를 쓴다.
- 비밀번호는 로그, 오류 메시지, `--json` 출력, 테스트 골든 파일에 평문으로 남기지 않는다.
- 외부 의존성은 3절에 적힌 것만 추가한다. 그 밖의 라이브러리는 추가 전에 묻는다.
- 모든 쓰기 경로는 `fsutil`을 거친다. 다른 곳에서 `os.WriteFile`로 두 파일을 직접 쓰지 않는다.
- 공개 함수에는 한 줄 이상의 문서 주석을 쓰고, 마일스톤을 끝낼 때마다 `make check`(gofmt 검사, `go vet`, `go test`)를 통과시킨 뒤 커밋한다.

**미정 사항 (구현 중 기본값으로 진행하되 확인 필요)**

- 서비스 파일 위치 기본값: `PGSERVICEFILE`이 없으면 `~/.pg_service.conf`. 시스템 전체 파일(`pg_config --sysconfdir`)은 읽지도 쓰지도 않는다.
- 비밀번호 없는 서비스(`trust`, 인증서 인증 등)를 허용한다. `add`에서 비밀번호를 비우면 pgpass 줄을 만들지 않는다.
- 같은 `host:port:dbname:user` 조합을 가진 서비스가 둘 이상일 때는 pgpass 줄이 하나로 충돌한다. 기본 동작은 경고 후 거부다.

