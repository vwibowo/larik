# Go dependency audit — 2026-11-07

Generated from `go list -m -u -json all` against the public Go module proxy. “Available” is the newest version reported by that command, not a recommendation to upgrade automatically. This is a version inventory; no dependency was changed.

31 direct and 199 transitive modules are in the build list. 15 direct and 98 transitive modules have a newer version available.

## Upgrade batches

- **Browser core:** test `chromedp` and `cdproto` together in a separate change, covering CLI/TUI navigation, screenshots, uploads, and browser panel behavior. They remain in Larik for standalone sessions.
- **TUI:** test Bubble Tea with its Charm dependencies together, including keyboard input, rendering, and browser panel behavior.
- **Providers and data:** upgrade SDKs and database drivers in separate batches with their fake-server integration tests. Keep the OpenAI Go SDK at `v3.68.0` until a newer version passes the existing plain-HTTP LAN compatibility requirement documented in `go.mod`.
- **MyGo:** `go.mod` names `v0.7.4`, but `replace` uses `third_party/mygo`. The Go module proxy reports a `v0.8.0` tag; compare the local OSC and notification-action patches in `third_party/mygo/README-LARIK.md` with that tag before updating the local copy.
- **Transitive modules:** let direct upgrades select compatible versions; do not bulk-update all transitive modules independently.

## Direct modules

| Module | Current | Available | Notes |
|---|---|---|---|
| `charm.land/bubbles/v2` | `v2.2.1` | `v2.2.1` | current |
| `charm.land/bubbletea/v2` | `v2.0.10` | `v2.1.1` |  |
| `charm.land/glamour/v2` | `v2.0.1` | `v2.0.1` | current |
| `charm.land/lipgloss/v2` | `v2.0.6` | `v2.0.6` | current |
| `charm.land/tone` | `v0.1.0` | `v0.1.0` | current |
| `github.com/JohannesKaufmann/html-to-markdown/v2` | `v2.5.2` | `v2.5.2` | current |
| `github.com/alecthomas/chroma/v2` | `v2.27.0` | `v2.27.0` | current |
| `github.com/anthropics/anthropic-sdk-go` | `v1.78.0` | `v1.84.0` |  |
| `github.com/bmatcuk/doublestar/v4` | `v4.10.2` | `v4.10.4` |  |
| `github.com/cespare/xxhash/v2` | `v2.3.0` | `v2.3.0` | current |
| `github.com/charmbracelet/harmonica` | `v0.2.0` | `v0.2.0` | current |
| `github.com/charmbracelet/ultraviolet` | `v0.0.0-20260922123528-4e49372c11f9` | `v0.0.0-20261023060817-a45b7c86333f` |  |
| `github.com/charmbracelet/x/ansi` | `v0.11.8` | `v0.11.9` |  |
| `github.com/chromedp/cdproto` | `v0.0.0-20260922220944-a19bff23514f` | `v0.159.2` |  |
| `github.com/chromedp/chromedp` | `v0.16.0` | `v0.23.0` |  |
| `github.com/dop251/goja` | `v0.0.0-20261002135814-104bc28c3abd` | `v0.0.0-20261105002551-2413f2132ac8` |  |
| `github.com/egoist/mygo` | `v0.7.4` | `v0.8.0` | local replace |
| `github.com/go-sql-driver/mysql` | `v1.9.3` | `v1.10.1` |  |
| `github.com/google/jsonschema-go` | `v0.4.3` | `v0.4.3` | current |
| `github.com/gorilla/websocket` | `v1.5.3` | `v1.5.3` | current |
| `github.com/jackc/pgx/v5` | `v5.12.0` | `v5.12.0` | current |
| `github.com/microsoft/go-mssqldb` | `v1.11.0` | `v1.11.2` |  |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | `v1.8.0` | current |
| `github.com/openai/openai-go/v3` | `v3.68.0` | `v3.96.0` | held for LAN HTTP compatibility |
| `golang.org/x/net` | `v0.59.0` | `v0.62.0` |  |
| `golang.org/x/oauth2` | `v0.37.0` | `v0.37.0` | current |
| `golang.org/x/sys` | `v0.49.0` | `v0.49.0` | current |
| `golang.org/x/term` | `v0.46.0` | `v0.47.0` |  |
| `google.golang.org/genai` | `v1.72.0` | `v1.77.0` |  |
| `gopkg.in/yaml.v3` | `v3.0.1` | `v3.0.1` | current |
| `modernc.org/sqlite` | `v1.61.1` | `v1.61.1` | current |

## Transitive modules

| Module | Current | Available | Notes |
|---|---|---|---|
| `cel.dev/expr` | `v0.25.2` | `v0.25.3` |  |
| `cloud.google.com/go` | `v0.116.0` | `v0.123.0` |  |
| `cloud.google.com/go/auth` | `v0.20.0` | `v0.24.3` |  |
| `cloud.google.com/go/auth/oauth2adapt` | `v0.2.8` | `v0.3.1` |  |
| `cloud.google.com/go/compute/metadata` | `v0.9.0` | `v0.10.0` |  |
| `cloud.google.com/go/iam` | `v1.2.0` | `v1.15.1` |  |
| `cloud.google.com/go/longrunning` | `v0.5.6` | `v1.5.0` |  |
| `cloud.google.com/go/storage` | `v1.43.0` | `v1.70.0` |  |
| `cloud.google.com/go/translate` | `v1.10.3` | `v1.20.1` |  |
| `filippo.io/edwards25519` | `v1.1.0` | `v1.2.0` |  |
| `github.com/Azure/azure-sdk-for-go/sdk/azcore` | `v1.23.1` | `v1.24.0` |  |
| `github.com/Azure/azure-sdk-for-go/sdk/azidentity` | `v1.14.1` | `v1.15.0` |  |
| `github.com/Azure/azure-sdk-for-go/sdk/internal` | `v1.12.0` | `v1.13.0` |  |
| `github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys` | `v1.5.0` | `v1.5.0` | current |
| `github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal` | `v1.2.0` | `v1.2.0` | current |
| `github.com/AzureAD/microsoft-authentication-library-for-go` | `v1.8.0` | `v1.10.1` |  |
| `github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp` | `v1.34.0` | `v1.38.0` |  |
| `github.com/JohannesKaufmann/dom` | `v0.3.1` | `v0.3.1` | current |
| `github.com/MakeNowJust/heredoc` | `v1.0.0` | `v1.0.0` | current |
| `github.com/Masterminds/semver/v3` | `v3.5.0` | `v3.5.0` | current |
| `github.com/agnivade/levenshtein` | `v1.2.1` | `v1.2.2` |  |
| `github.com/alecthomas/assert/v2` | `v2.11.0` | `v2.11.0` | current |
| `github.com/alecthomas/repr` | `v0.5.2` | `v0.5.4` |  |
| `github.com/andybalholm/cascadia` | `v1.3.4` | `v1.3.5` |  |
| `github.com/atotto/clipboard` | `v0.1.4` | `v0.1.4` | current |
| `github.com/aws/aws-sdk-go-v2` | `v1.47.0` | `v1.47.7` |  |
| `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` | `v1.6.3` | `v1.7.26` |  |
| `github.com/aws/aws-sdk-go-v2/config` | `v1.33.4` | `v1.33.15` |  |
| `github.com/aws/aws-sdk-go-v2/credentials` | `v1.20.4` | `v1.20.15` |  |
| `github.com/aws/aws-sdk-go-v2/feature/ec2/imds` | `v1.20.0` | `v1.20.7` |  |
| `github.com/aws/aws-sdk-go-v2/internal/configsources` | `v1.5.3` | `v1.5.10` |  |
| `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | `v2.8.3` | `v2.8.10` |  |
| `github.com/aws/aws-sdk-go-v2/internal/ini` | `v1.8.0` | `v1.8.6` |  |
| `github.com/aws/aws-sdk-go-v2/internal/v4a` | `v1.5.3` | `v1.5.10` |  |
| `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | `v1.13.19` | `v1.13.24` |  |
| `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | `v1.14.3` | `v1.14.10` |  |
| `github.com/aws/aws-sdk-go-v2/service/signin` | `v1.10.0` | `v1.12.3` |  |
| `github.com/aws/aws-sdk-go-v2/service/sso` | `v1.38.0` | `v1.39.3` |  |
| `github.com/aws/aws-sdk-go-v2/service/ssooidc` | `v1.43.0` | `v1.44.3` |  |
| `github.com/aws/aws-sdk-go-v2/service/sts` | `v1.50.0` | `v1.51.8` |  |
| `github.com/aws/smithy-go` | `v1.28.1` | `v1.28.8` |  |
| `github.com/aymanbagabas/go-osc52/v2` | `v2.0.1` | `v2.0.1` | current |
| `github.com/aymanbagabas/go-udiff` | `v0.4.1` | `v0.4.1` | current |
| `github.com/aymerick/douceur` | `v0.2.0` | `v0.2.0` | current |
| `github.com/bahlo/generic-list-go` | `v0.2.0` | `v0.2.0` | current |
| `github.com/bits-and-blooms/bitset` | `v1.24.6` | `v1.29.0` |  |
| `github.com/buger/jsonparser` | `v1.1.2` | `v1.6.1` |  |
| `github.com/charmbracelet/colorprofile` | `v0.4.3` | `v0.4.3` | current |
| `github.com/charmbracelet/x/exp/golden` | `v0.0.0-20250806222409-83e3a29d542f` | `v0.1.0` |  |
| `github.com/charmbracelet/x/exp/slice` | `v0.0.0-20250327172914-2fdc97757edf` | `v0.1.0` |  |
| `github.com/charmbracelet/x/term` | `v0.2.2` | `v0.2.2` | current |
| `github.com/charmbracelet/x/termios` | `v0.1.1` | `v0.1.1` | current |
| `github.com/charmbracelet/x/windows` | `v0.2.2` | `v0.2.2` | current |
| `github.com/chromedp/sysutil` | `v1.1.0` | `v1.1.0` | current |
| `github.com/chzyer/readline` | `v1.5.1` | `v1.5.1` | current |
| `github.com/clipperhouse/displaywidth` | `v0.11.0` | `v0.11.0` | current |
| `github.com/clipperhouse/stringish` | `v0.1.1` | `v0.1.1` | current |
| `github.com/clipperhouse/uax29/v2` | `v2.7.0` | `v2.7.0` | current |
| `github.com/cncf/xds/go` | `v0.0.0-20260202195803-dba9d589def2` | `v0.0.0-20260202195803-dba9d589def2` | current |
| `github.com/coder/websocket` | `v1.8.15` | `v1.8.15` | current |
| `github.com/creack/pty` | `v1.1.24` | `v1.1.24` | current |
| `github.com/davecgh/go-spew` | `v1.1.1` | `v1.1.1` | current |
| `github.com/dlclark/regexp2` | `v1.11.0` | `v1.12.0` |  |
| `github.com/dlclark/regexp2/v2` | `v2.8.1` | `v2.12.0` |  |
| `github.com/dnaeon/go-vcr` | `v1.2.0` | `v1.2.0` | current |
| `github.com/dop251/goja_nodejs` | `v0.0.0-20211022123610-8dd9abb0616d` | `v0.0.0-20260918173711-b481721df8a2` |  |
| `github.com/dustin/go-humanize` | `v1.1.0` | `v1.1.0` | current |
| `github.com/ebitengine/purego` | `v0.11.1` | `v0.11.3` |  |
| `github.com/eliben/go-sentencepiece` | `v0.7.0` | `v0.7.0` | current |
| `github.com/envoyproxy/go-control-plane` | `v0.14.0` | `v0.14.0` | current |
| `github.com/envoyproxy/go-control-plane/envoy` | `v1.37.0` | `v1.40.0` |  |
| `github.com/envoyproxy/go-control-plane/ratelimit` | `v0.1.0` | `v0.1.0` | current |
| `github.com/envoyproxy/protoc-gen-validate` | `v1.3.3` | `v1.3.3` | current |
| `github.com/felixge/httpsnoop` | `v1.1.0` | `v1.1.0` | current |
| `github.com/go-jose/go-jose/v4` | `v4.1.4` | `v4.1.9` |  |
| `github.com/go-json-experiment/json` | `v0.0.0-20260820222146-c27c302e5fc3` | `v0.0.0-20260820222146-c27c302e5fc3` | current |
| `github.com/go-logr/logr` | `v1.4.3` | `v1.4.4` |  |
| `github.com/go-logr/stdr` | `v1.2.2` | `v1.2.2` | current |
| `github.com/go-sourcemap/sourcemap` | `v2.1.3+incompatible` | `v2.1.4+incompatible` |  |
| `github.com/go-text/typesetting` | `v0.3.5` | `v0.3.5` | current |
| `github.com/go-text/typesetting-utils` | `v0.0.0-20260419141703-4ffe8874dabc` | `v0.0.0-20261009215314-bb530f4842f3` |  |
| `github.com/gobwas/httphead` | `v0.1.0` | `v0.1.0` | current |
| `github.com/gobwas/pool` | `v0.2.1` | `v0.2.1` | current |
| `github.com/gobwas/ws` | `v1.4.0` | `v1.5.0` |  |
| `github.com/goccy/go-yaml` | `v1.19.2` | `v1.19.2` | current |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` | `v5.3.1` | current |
| `github.com/golang-sql/civil` | `v0.0.0-20220223132316-b832511892a9` | `v0.0.0-20220223132316-b832511892a9` | current |
| `github.com/golang-sql/sqlexp` | `v0.1.0` | `v0.1.0` | current |
| `github.com/golang/glog` | `v1.2.5` | `v1.2.5` | current |
| `github.com/golang/groupcache` | `v0.0.0-20210331224755-41bb18bfe9da` | `v0.0.0-20241129210726-2c02b8208cf8` |  |
| `github.com/golang/protobuf` | `v1.5.4` | `v1.5.4` | current |
| `github.com/golang/snappy` | `v0.0.4` | `v1.0.0` |  |
| `github.com/google/go-cmp` | `v0.7.0` | `v0.7.0` | current |
| `github.com/google/go-pkcs11` | `v0.3.0` | `v0.3.0` | current |
| `github.com/google/martian/v3` | `v3.3.3` | `v3.3.3` | current |
| `github.com/google/pprof` | `v0.0.0-20261016150608-1facf8f91b50` | `v0.0.0-20261105003125-4bf881d7fb0b` |  |
| `github.com/google/s2a-go` | `v0.1.9` | `v0.1.11` |  |
| `github.com/google/uuid` | `v1.6.0` | `v1.6.0` | current |
| `github.com/googleapis/enterprise-certificate-proxy` | `v0.3.15` | `v0.3.25` |  |
| `github.com/googleapis/gax-go/v2` | `v2.22.0` | `v2.27.0` |  |
| `github.com/gorilla/css` | `v1.0.1` | `v1.0.1` | current |
| `github.com/hajimehoshi/go-mp3` | `v0.3.4` | `v0.3.4` | current |
| `github.com/hashicorp/go-uuid` | `v1.0.3` | `v1.0.4` |  |
| `github.com/hashicorp/golang-lru/v2` | `v2.0.7` | `v2.0.7` | current |
| `github.com/hexops/gotextdiff` | `v1.0.3` | `v1.0.3` | current |
| `github.com/ianlancetaylor/demangle` | `v0.0.0-20250417193237-f615e6bd150b` | `v0.0.0-20261012173340-cbb8aaea4290` |  |
| `github.com/invopop/jsonschema` | `v0.14.0` | `v0.14.0` | current |
| `github.com/jackc/pgpassfile` | `v1.0.0` | `v1.0.0` | current |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | `v0.0.0-20240606120523-5a60cdf6a761` | current |
| `github.com/jackc/puddle/v2` | `v2.2.3` | `v2.2.3` | current |
| `github.com/jcmturner/aescts/v2` | `v2.0.0` | `v2.0.0` | current |
| `github.com/jcmturner/dnsutils/v2` | `v2.0.0` | `v2.0.0` | current |
| `github.com/jcmturner/gofork` | `v1.7.6` | `v1.7.6` | current |
| `github.com/jcmturner/goidentity/v6` | `v6.0.1` | `v6.0.1` | current |
| `github.com/jcmturner/gokrb5/v8` | `v8.4.4` | `v8.4.4` | current |
| `github.com/jcmturner/rpc/v2` | `v2.0.3` | `v2.0.3` | current |
| `github.com/jfreymuth/oggvorbis` | `v1.0.5` | `v1.0.5` | current |
| `github.com/jfreymuth/vorbis` | `v1.0.2` | `v1.0.2` | current |
| `github.com/kr/pretty` | `v0.3.1` | `v0.3.1` | current |
| `github.com/kr/text` | `v0.2.0` | `v0.2.0` | current |
| `github.com/kylelemons/godebug` | `v1.1.0` | `v1.1.0` | current |
| `github.com/ledongthuc/pdf` | `v0.0.0-20220302134840-0c2507a12d80` | `v0.0.0-20261020125853-5e46805f718e` |  |
| `github.com/lucasb-eyer/go-colorful` | `v1.4.1` | `v1.4.1` | current |
| `github.com/mattn/go-isatty` | `v0.0.24` | `v0.0.24` | current |
| `github.com/mattn/go-runewidth` | `v0.0.30` | `v0.0.31` |  |
| `github.com/microcosm-cc/bluemonday` | `v1.0.27` | `v1.0.27` | current |
| `github.com/muesli/cancelreader` | `v0.2.2` | `v0.2.2` | current |
| `github.com/muesli/termenv` | `v0.16.0` | `v0.16.0` | current |
| `github.com/ncruces/go-strftime` | `v1.1.0` | `v1.1.0` | current |
| `github.com/orisano/pixelmatch` | `v0.0.0-20220722002657-fb0b55479cde` | `v0.0.0-20261015095729-8d350b6cff2e` |  |
| `github.com/pb33f/ordered-map/v2` | `v2.3.1` | `v2.3.2` |  |
| `github.com/pkg/browser` | `v0.0.0-20240102092130-5ac0b6a4141c` | `v0.0.0-20240102092130-5ac0b6a4141c` | current |
| `github.com/planetscale/vtprotobuf` | `v0.6.1-0.20240319094008-0393e58bdf10` | `v0.6.1-0.20240319094008-0393e58bdf10` | current |
| `github.com/pmezard/go-difflib` | `v1.0.0` | `v1.0.0` | current |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | `v0.0.0-20230129092748-24d4a6f8daec` | current |
| `github.com/rivo/uniseg` | `v0.4.7` | `v0.4.7` | current |
| `github.com/rogpeppe/go-internal` | `v1.14.1` | `v1.16.0` |  |
| `github.com/sahilm/fuzzy` | `v0.1.3` | `v0.1.3` | current |
| `github.com/sebdah/goldie/v2` | `v2.8.0` | `v2.8.0` | current |
| `github.com/segmentio/asm` | `v1.1.3` | `v1.2.1` |  |
| `github.com/segmentio/encoding` | `v0.5.4` | `v0.5.4` | current |
| `github.com/sergi/go-diff` | `v1.4.0` | `v1.4.0` | current |
| `github.com/shopspring/decimal` | `v1.4.0` | `v1.6.0` |  |
| `github.com/spiffe/go-spiffe/v2` | `v2.8.1` | `v2.9.0` |  |
| `github.com/standard-webhooks/standard-webhooks/libraries` | `v0.0.1` | `v0.0.1` | current |
| `github.com/stretchr/objx` | `v0.1.0` | `v0.8.0` |  |
| `github.com/stretchr/testify` | `v1.12.1` | `v1.12.1` | current |
| `github.com/tidwall/gjson` | `v1.19.0` | `v1.20.1` |  |
| `github.com/tidwall/match` | `v1.1.1` | `v1.2.0` |  |
| `github.com/tidwall/pretty` | `v1.2.1` | `v1.2.2` |  |
| `github.com/tidwall/sjson` | `v1.2.5` | `v1.2.7` |  |
| `github.com/xo/terminfo` | `v1.0.0` | `v1.3.1` |  |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | `v3.0.2` | current |
| `github.com/yuin/goldmark` | `v1.8.2` | `v1.8.7` |  |
| `github.com/yuin/goldmark-emoji` | `v1.0.5` | `v1.0.6` |  |
| `go.opencensus.io` | `v0.24.0` | `v0.24.0` | current |
| `go.opentelemetry.io/auto/sdk` | `v1.2.1` | `v1.2.1` | current |
| `go.opentelemetry.io/contrib/detectors/gcp` | `v1.44.0` | `v1.47.0` |  |
| `go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc` | `v0.67.0` | `v0.72.0` |  |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | `v0.69.0` | `v0.72.0` |  |
| `go.opentelemetry.io/otel` | `v1.44.0` | `v1.47.0` |  |
| `go.opentelemetry.io/otel/metric` | `v1.44.0` | `v1.47.0` |  |
| `go.opentelemetry.io/otel/sdk` | `v1.44.0` | `v1.47.0` |  |
| `go.opentelemetry.io/otel/sdk/metric` | `v1.44.0` | `v1.47.0` |  |
| `go.opentelemetry.io/otel/trace` | `v1.44.0` | `v1.47.0` |  |
| `go.yaml.in/yaml/v3` | `v3.0.5` | `v3.0.6` |  |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.2` | `v4.0.0` |  |
| `golang.org/x/crypto` | `v0.57.0` | `v0.58.0` |  |
| `golang.org/x/exp` | `v0.0.0-20231006140011-7918f672742d` | `v0.0.0-20261106112924-d498a4c2cc9a` |  |
| `golang.org/x/image` | `v0.46.0` | `v0.47.0` |  |
| `golang.org/x/mod` | `v0.42.0` | `v0.42.0` | current |
| `golang.org/x/sync` | `v0.24.0` | `v0.24.0` | current |
| `golang.org/x/text` | `v0.43.0` | `v0.43.0` | current |
| `golang.org/x/time` | `v0.15.0` | `v0.16.0` |  |
| `golang.org/x/tools` | `v0.52.0` | `v0.52.0` | current |
| `gonum.org/v1/gonum` | `v0.17.0` | `v0.17.0` | current |
| `google.golang.org/api` | `v0.278.0` | `v0.311.0` |  |
| `google.golang.org/appengine` | `v1.6.8` | `v1.6.8` | current |
| `google.golang.org/genproto` | `v0.0.0-20260319201613-d00831a3d3e7` | `v0.0.0-20261104224025-d1e00e9f33ab` |  |
| `google.golang.org/genproto/googleapis/api` | `v0.0.0-20260706201446-f0a921348800` | `v0.0.0-20261104224025-d1e00e9f33ab` |  |
| `google.golang.org/genproto/googleapis/bytestream` | `v0.0.0-20260427160629-7cedc36a6bc4` | `v0.0.0-20261104224025-d1e00e9f33ab` |  |
| `google.golang.org/genproto/googleapis/rpc` | `v0.0.0-20260706201446-f0a921348800` | `v0.0.0-20261104224025-d1e00e9f33ab` |  |
| `google.golang.org/grpc` | `v1.84.0` | `v1.85.0` |  |
| `google.golang.org/protobuf` | `v1.36.11` | `v1.36.12` |  |
| `gopkg.in/check.v1` | `v1.0.0-20201130134442-10cb98267c6c` | `v1.0.0-20201130134442-10cb98267c6c` | current |
| `gopkg.in/yaml.v2` | `v2.2.8` | `v2.4.0` |  |
| `modernc.org/cc/v4` | `v4.30.3` | `v4.31.0` |  |
| `modernc.org/ccgo/v4` | `v4.37.0` | `v4.38.1` |  |
| `modernc.org/fileutil` | `v1.4.0` | `v1.4.0` | current |
| `modernc.org/gc/v2` | `v2.6.5` | `v2.6.5` | current |
| `modernc.org/gc/v3` | `v3.2.0` | `v3.2.1` |  |
| `modernc.org/goabi0` | `v0.2.0` | `v0.2.0` | current |
| `modernc.org/libc` | `v1.79.0` | `v1.80.1` |  |
| `modernc.org/mathutil` | `v1.7.1` | `v1.7.1` | current |
| `modernc.org/memory` | `v1.12.1` | `v1.12.1` | current |
| `modernc.org/opt` | `v0.2.0` | `v0.2.0` | current |
| `modernc.org/sortutil` | `v1.2.1` | `v1.2.1` | current |
| `modernc.org/strutil` | `v1.2.1` | `v1.2.1` | current |
| `modernc.org/token` | `v1.1.0` | `v1.1.0` | current |
