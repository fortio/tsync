# WARP.md

This file provides guidance to WARP (warp.dev) when working with code in this repository.

**Last updated**: v0.8.0 (commit 12564c16a0f725e447aa2880e57b30ff282dc112)

## Project Overview

tsync is a cross-platform terminal UI and network-based synchronization tool for clipboard and files. It's a work-in-progress Go application that uses multicast UDP for peer discovery and Ed25519 cryptography for secure identity management.

## Development Commands

### Building and Running
```bash
# Build the binary
CGO_ENABLED=0 go build -o tsync .

# Run directly without installing
CGO_ENABLED=0 go run .

# Install to $GOPATH/bin (typically ~/go/bin)
CGO_ENABLED=0 go install .

# Run with custom parameters
go run . -name "MyMachine" -port 29556 -mcast "239.255.116.115"
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests with race detector (important for concurrent code)
go test -race ./...

# Run tests in a specific package
go test ./tcrypto

# Run a specific test
go test ./tcrypto -run TestIdentity

# Generate test coverage
go test -cover ./...
```

### Code Quality
The project uses standard Go tooling and GitHub Actions for CI/CD:
```bash
# Format code
gofumpt -w *.go */*.go

# Run golangci-lint for comprehensive linting
golangci-lint run

# Run all tests
go test -v -cover ./...
```

## Architecture Overview

### Core Components

**Main Application (`main.go`)**
- Entry point that initializes the terminal UI using `fortio.org/terminal/ansipixels`
- Manages cryptographic identity loading/creation
- Orchestrates the network server and peer discovery display
- Handles terminal input (Q/q/Ctrl-C to quit, 1-9 to connect to peers)
- Implements tabular display of peers with proper formatting and alignment

**Network Layer (`tsnet/`)**
- `Server`: Core networking component handling multicast UDP discovery
- Peer discovery via multicast broadcasts (default 239.255.116.115:29556)
- Interface detection to find the correct network interface for multicast
- Automatic peer cleanup based on configurable timeout (10s default)
- Uses epoch-based messaging to detect and handle duplicate instances

**Cryptographic Identity (`tcrypto/`)**
- Ed25519-based identity system for peer authentication
- `Identity`: Manages public/private key pairs with string encoding/decoding
- `HumanHash`: Creates human-readable fingerprints from public keys
- Message signing and verification capabilities
- File-based identity persistence in `~/.tsync/`
- **Security Architecture**: All encryption/security is handled in `tcrypto`, NOT in `tsnet`
  - Ephemeral keys for secure connections
  - HKDF (HMAC-based Key Derivation Function) for key derivation
  - Human hash verification before link validation (TOFU - Trust On First Use)
  - `tsnet` remains focused on networking; `tcrypto` handles all cryptographic operations

**Table Rendering (`table/`)**
- Custom table rendering system for terminal UI display
- Supports multiple alignment options (Left, Center, Right)
- Multiple border styles (None, Columns, Outer, OuterColumns, Full)
- Integrates with `fortio.org/terminal/ansipixels` for terminal output

**Synchronized Map (`smap/`)**
- Thread-safe map implementation for managing discovered peers
- Note: This is deprecated in favor of `fortio.org/smap` external module

### Network Protocol

**Discovery Protocol**:
- Format: `"tsync1 %q <public_key> e <epoch>"` (name is quoted for safety)
- Broadcasts every ~1.5s with random jitter (0-1s) to avoid collision
- Peers timeout after 10s of no messages
- Automatic interface detection by testing connectivity to 8.8.8.8:53
- Enhanced interface debugging for troubleshooting network issues

**Key Features**:
- Cross-platform multicast UDP networking
- Automatic duplicate detection (same name/IP/key)
- Dynamic peer management with cleanup
- Terminal UI with real-time tabular peer display
- Interactive peer selection (keys 1-9 bind to discovered peers)
- Stable peer snapshot system for consistent UI display

### Data Flow

1. **Startup**: Load/create Ed25519 identity from `~/.tsync/`
2. **Network Init**: Detect correct interface, bind multicast listeners
3. **Discovery Loop**: Broadcast identity, receive peer messages
4. **UI Loop**: Display peers in tabular format, handle user input (including peer connections)
5. **Peer Interaction**: Keys 1-9 trigger connection attempts to corresponding peers
6. **Cleanup**: Remove expired peers, graceful shutdown on exit

### Key Dependencies

- `fortio.org/cli`: Command-line parsing and setup
- `fortio.org/log`: Structured logging throughout
- `fortio.org/terminal/ansipixels`: Terminal UI and color management
- `fortio.org/smap`: Thread-safe map for peer storage with snapshot support
- Standard library: `crypto/ed25519`, `net` for networking, `slices` for sorting

## Coding Style Guidelines

### Minimizing Diffs
When making changes to the codebase, follow these practices to minimize diffs and improve code review:

- **Add new struct fields at the end**: Place new fields at the bottom of struct definitions to avoid changing indentation of existing fields
- **Use comment separators**: Add a comment line before new logical sections (e.g., `// Direct peer connections`)
- **Preserve existing indentation**: Don't reformat existing code unless necessary
- **Incremental changes**: Make small, focused commits that change only what's needed
- **Order matters**: When adding new code, consider placement that minimizes line number changes

**Example of good practice**:
```go
type Server struct {
    // Existing fields (unchanged)
    Config
    ourSendAddr *net.UDPAddr
    destAddr    *net.UDPAddr
    // ... other existing fields ...

    // Direct peer connections - new section
    unicastListen *net.UDPConn
    connections   *smap.Map[Peer, Connection]
}
```

This approach adds only 3 new lines instead of reformatting the entire struct.

## Development Notes

- The application is designed to work across Windows, macOS, and Linux
- Windows requires special interface detection due to WSL virtual interfaces
- All networking uses IPv4 UDP multicast
- Cryptographic operations use Ed25519 for performance and security
- Terminal UI supports resize events and FPS-limited refresh with table-based layout
- Docker support available but multicast may not work in all environments
- Enhanced debugging available for interface detection and network troubleshooting
- Peer interaction system allows direct connection attempts via numbered keys
