# AGENTS.md - dir2opds

This file provides guidelines for AI coding agents working on the dir2opds codebase.

## Build, Test, and Lint Commands

### Building
```bash
# Build for current platform (default target)
make build

# Build for all platforms (Darwin, FreeBSD, Linux, Windows, etc.)
make build-all

# Build with Go directly
go build .
```

### Build Artifacts
- Do not automatically generate Windows `.exe` files unless the user explicitly asks to produce an executable.
- When the user asks to produce the Windows executable, generate only `dir2opds-gui.exe` by default.
- Place compiled Windows `.exe` files in `release/<version>/` under the repository root (for example, `release/1.2.3/dir2opds-gui.exe`), using the version being built.
- Place all release deliverables for the same version in that `release/<version>/` directory, including executables for other platforms, distribution archives, installers, and accompanying release files.
- Before publishing a release, review and update the README, usage instructions, and release notes so documented features, defaults, options, and limitations match the actual behavior of that version. Do not describe planned or unsupported functionality as available.
- Do not generate or distribute `dir2opds.exe` unless the user explicitly asks for the non-GUI CLI executable.

### Docker
- When rebuilding or restarting Docker Compose for this repository, run it from WSL in `/mnt/d/github/dir2opds`.
- Use the pattern `wsl bash -lc "cd /mnt/d/github/dir2opds && docker compose up -d --build dir2opds"` so relative compose paths resolve consistently.

### Testing
```bash
# Run all tests
go test ./...
go test -v ./...

# Run a single test
go test -v -run TestHandler ./internal/service/
go test -v -run TestScan ./internal/service/

# Run tests with coverage
go test -cover ./...
```

### Linting and Formatting
```bash
# Format code (uses go fmt)
make fmt

# Run go vet
make vet

# Full build pipeline (fmt -> vet -> build)
make
```

### Cleaning
```bash
make clean
```

## Code Style Guidelines

### General Go Conventions
- Follow standard Go formatting (`go fmt`)
- Pass `go vet` without warnings
- Use Go 1.25.3+ (see go.mod)
- Maximum line length: aim for readability, no strict limit

### Imports
- Group imports: stdlib first, then external packages, then internal
- Use blank imports only when necessary
- Example:
```go
import (
    "archive/zip"
    "encoding/xml"
    "log/slog"
    
    "github.com/lann/builder"
    "golang.org/x/tools/blog/atom"
    
    "github.com/SamLaio/dir2opds/opds"
)
```

### Naming Conventions
- **Packages**: lowercase, single word (e.g., `service`, `opds`)
- **Exported**: PascalCase (e.g., `Handler`, `Scan`)
- **Unexported**: camelCase (e.g., `extractMetadata`, `sortEntries`)
- **Constants**: camelCase for unexported, PascalCase for exported
- **Test files**: `*_test.go` suffix, package name suffixed with `_test` for external tests
- **Interfaces**: Noun ending in "-er" (e.g., `IsDirer`)

### Types and Structs
- Use struct tags for XML/JSON marshaling
- Use `iota` for related constants
- Document public types with comments starting with the type name

### Error Handling
- Always check errors and handle appropriately
- Use `fmt.Errorf` with `%w` verb for error wrapping
- Log errors using `slog.Error()` with structured attributes
- Return errors rather than swallowing them
- Example:
```go
if err != nil {
    slog.Error("operation failed", "error", err)
    return fmt.Errorf("doing operation: %w", err)
}
```

### Logging
- Use `log/slog` for all logging (structured logging)
- Use key-value pairs: `slog.Info("message", "key", value)`
- Prefer `slog.Debug` for verbose output
- Use JSON handler by default, text handler optional

### Builder Pattern
The project uses `github.com/lann/builder` for immutable builders:
- Define builder types (e.g., `type feedBuilder builder.Builder`)
- Each setter returns the builder for chaining
- End with `Build()` to get the final struct
- Export a singleton instance (e.g., `var FeedBuilder`)

### Testing Patterns
- Use table-driven tests with `map[string]struct{...}`
- Use `t.Run(name, func(t *testing.T){...})` for subtests
- Prefer `testify/assert` and `testify/require`
- Use `httptest` for HTTP handler tests
- Store test data in `testdata/` directories
- Mock time using injectable functions (e.g., `TimeNow`)

### HTTP Handlers
- Return errors from handlers instead of writing directly to ResponseWriter
- Use `errorHandler` wrapper for consistent error responses
- Verify paths to prevent directory traversal attacks
- Set appropriate Content-Type headers

### Security
- Always use `verifyPath()` to prevent path traversal
- Use `filepath.Clean()` and `filepath.EvalSymlinks()` for path sanitization
- Check that paths are within `TrustedRoot` before accessing filesystem

### Comments and Documentation
- Package comments should describe the package purpose
- Start with `// Package name ...`
- Public functions/methods must have documentation comments
- Comments should start with the name being documented

### Commit Messages
Before every commit, review and update the README so its feature descriptions, defaults, options, and limitations match the actual code being committed. Increment the version for every commit using the repository's existing version naming convention, and keep version references consistent across the README, changelog, and release notes where present.

Every push must include a version tag pointing to the commit being pushed. Follow the repository's existing version naming convention, use a new version for new changes, and push the tag together with the commits. Never overwrite an existing version tag.
Before every push, prepare a non-empty local `release/<version>/RELEASE_NOTES.md` describing that version's actual changes and limitations, along with its intended release attachments. The version directory must exactly match the tag. The entire `release/` directory stays local and gitignored; never force-add or commit its contents.
After tests pass and the version tag is pushed, publish from the local machine with `pwsh -File scripts/publish-release.ps1 -Version <version>`. This uploads the local Markdown as the GitHub Release description and all files in that version directory as release assets, without adding them to Git history. Only intended public release files belong in this directory. GitHub Actions cannot read these local files and must not attempt to publish them from a checkout.

Follow [Conventional Commits](https://www.conventionalcommits.org/):
- `feat: add new feature`
- `fix: resolve bug`
- `docs: update documentation`
- `test: add tests`
- `refactor: restructure code`

## Project Structure

```
.
├── main.go                    # Entry point, CLI flags, HTTP routing
├── main_test.go               # Main package tests
├── internal/
│   └── service/
│       ├── service.go         # Core business logic (OPDS struct, handlers)
│       ├── service_test.go    # External tests (package service_test)
│       ├── internal_test.go   # Internal tests (package service)
│       ├── gzip.go            # Gzip compression middleware
│       ├── gzip_test.go       # Gzip middleware tests
│       ├── health.go          # Health check endpoint
│       ├── health_test.go     # Health endpoint tests
│       └── testdata/          # Test fixtures
├── opds/                      # OPDS/Atom XML builders
│   ├── feed_builder.go        # Feed builder (immutable)
│   ├── entry_builder.go       # Entry builder
│   ├── link_builder.go        # Link builder
│   ├── author_builder.go      # Author builder
│   ├── text_builder.go        # Text builder
│   └── doc.go                 # Package documentation
├── files/                     # Platform-specific files (systemd, etc.)
├── Makefile                   # Build automation
└── go.mod                     # Go module definition
```

## Key Dependencies
- `github.com/lann/builder` - Immutable builders
- `github.com/stretchr/testify` - Testing utilities
- `golang.org/x/tools/blog/atom` - Atom feed structs

## Architecture Notes

### Request Flow
```
HTTP Request → errorHandler wrapper → OPDS.Handler()
    ├─ File: http.ServeFile()
    └─ Directory: Scan() → makeFeed() → XML response
```

### Key Structs
- `OPDS` - Main service struct with configuration (TrustedRoot, flags, etc.)
- `Catalog` - Directory listing with entries, pagination info
- `CatalogEntry` - Individual file/folder entry

### Extension Points
- `extractMetadata()` - Add new file format parsers
- `MimeMap` - Custom MIME type mappings via `-mime-map` flag
- `GzipMiddleware` - Compression middleware (optional via `-gzip` flag)
