#!/usr/bin/env bash
# Development Environment Health Check for the Optimism Monorepo
#
# This script verifies that all required development dependencies are properly
# installed and configured. Run this after cloning the repository or when
# experiencing build/test issues.
#
# Usage:
#   ./ops/scripts/check-dev-environment.sh [--fix] [--verbose]
#
# Options:
#   --fix       Attempt to fix common issues automatically
#   --verbose   Show detailed version information
#   --json      Output results as JSON for CI integration

set -uo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
VERSIONS_FILE="$REPO_ROOT/versions.json"

# Flags
FIX_MODE=false
VERBOSE=false
JSON_OUTPUT=false

# Results tracking
declare -A RESULTS
WARNINGS=0
ERRORS=0

# Parse arguments
for arg in "$@"; do
    case $arg in
        --fix)
            FIX_MODE=true
            shift
            ;;
        --verbose)
            VERBOSE=true
            shift
            ;;
        --json)
            JSON_OUTPUT=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [--fix] [--verbose] [--json]"
            echo ""
            echo "Options:"
            echo "  --fix       Attempt to fix common issues automatically"
            echo "  --verbose   Show detailed version information"
            echo "  --json      Output results as JSON for CI integration"
            exit 0
            ;;
    esac
done

# Utility functions
print_header() {
    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo ""
        echo -e "${BLUE}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${CYAN}${BOLD}  $1${NC}"
        echo -e "${BLUE}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    fi
}

print_check() {
    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo -e "${YELLOW}▶${NC} Checking: $1"
    fi
}

print_ok() {
    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo -e "  ${GREEN}✓${NC} $1"
    fi
}

print_warn() {
    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo -e "  ${YELLOW}!${NC} $1"
    fi
    ((WARNINGS++))
}

print_err() {
    if [[ "$JSON_OUTPUT" == "false" ]]; then
        echo -e "  ${RED}✗${NC} $1"
    fi
    ((ERRORS++))
}

print_info() {
    if [[ "$JSON_OUTPUT" == "false" ]] && [[ "$VERBOSE" == "true" ]]; then
        echo -e "  ${MAGENTA}ℹ${NC} $1"
    fi
}

# Version comparison function
version_gte() {
    # Returns 0 if $1 >= $2
    printf '%s\n%s' "$2" "$1" | sort -V -C
}

version_extract() {
    # Extract version number from version string
    echo "$1" | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1
}

# Check if command exists
check_command() {
    local cmd="$1"
    local name="$2"
    local min_version="${3:-}"
    local version_cmd="${4:-$cmd --version}"
    local install_hint="${5:-}"
    
    print_check "$name"
    
    if ! command -v "$cmd" &> /dev/null; then
        print_err "$name is not installed"
        RESULTS["$name"]="missing"
        if [[ -n "$install_hint" ]]; then
            echo -e "      ${YELLOW}Install: $install_hint${NC}"
        fi
        return 1
    fi
    
    # Get version
    local version_output
    version_output=$(eval "$version_cmd" 2>&1 | head -1)
    local version
    version=$(version_extract "$version_output")
    
    if [[ -n "$min_version" ]] && [[ -n "$version" ]]; then
        if version_gte "$version" "$min_version"; then
            print_ok "$name v$version (>= $min_version required)"
            RESULTS["$name"]="ok"
        else
            print_warn "$name v$version (>= $min_version recommended)"
            RESULTS["$name"]="outdated"
        fi
    else
        print_ok "$name installed ($version_output)"
        RESULTS["$name"]="ok"
    fi
    
    print_info "Path: $(which "$cmd")"
    
    return 0
}

# ============================================================================
# Main Checks
# ============================================================================

cd "$REPO_ROOT"

if [[ "$JSON_OUTPUT" == "false" ]]; then
    echo -e "${BOLD}Optimism Development Environment Health Check${NC}"
    echo -e "Repository: ${CYAN}$REPO_ROOT${NC}"
    echo ""
fi

# ============================================================================
# Core Development Tools
# ============================================================================
print_header "Core Development Tools"

check_command "git" "Git" "2.0" "git --version" "https://git-scm.com/"

check_command "go" "Go" "1.21" "go version" "https://go.dev/doc/install"

check_command "node" "Node.js" "20.0" "node --version" "https://nodejs.org/"

check_command "pnpm" "pnpm" "8.0" "pnpm --version" "npm install -g pnpm"

check_command "make" "Make" "3.0" "make --version" "Install via system package manager"

check_command "jq" "jq" "1.6" "jq --version" "Install via system package manager"

# ============================================================================
# Blockchain Development Tools
# ============================================================================
print_header "Blockchain Development Tools"

# Check for foundry with pinned version
print_check "Foundry (forge)"
if command -v forge &> /dev/null; then
    FORGE_VERSION=$(forge --version 2>&1 | head -1)
    
    # Check if versions.json exists and has foundry version
    if [[ -f "$VERSIONS_FILE" ]]; then
        EXPECTED_FOUNDRY=$(jq -r '.foundry // empty' "$VERSIONS_FILE" 2>/dev/null)
        if [[ -n "$EXPECTED_FOUNDRY" ]]; then
            if echo "$FORGE_VERSION" | grep -q "$EXPECTED_FOUNDRY"; then
                print_ok "Foundry version matches pinned version"
                RESULTS["Foundry"]="ok"
            else
                print_warn "Foundry version mismatch"
                echo -e "      ${YELLOW}Expected: $EXPECTED_FOUNDRY${NC}"
                echo -e "      ${YELLOW}Found: $FORGE_VERSION${NC}"
                echo -e "      ${YELLOW}Run: pnpm update:foundry${NC}"
                RESULTS["Foundry"]="mismatch"
            fi
        else
            print_ok "Foundry installed ($FORGE_VERSION)"
            RESULTS["Foundry"]="ok"
        fi
    else
        print_ok "Foundry installed ($FORGE_VERSION)"
        RESULTS["Foundry"]="ok"
    fi
    print_info "Path: $(which forge)"
else
    print_err "Foundry is not installed"
    RESULTS["Foundry"]="missing"
    echo -e "      ${YELLOW}Install: curl -L https://foundry.paradigm.xyz | bash${NC}"
fi

check_command "cast" "Cast (Foundry)" "" "cast --version"

check_command "anvil" "Anvil (Foundry)" "" "anvil --version"

# ============================================================================
# Optional Tools
# ============================================================================
print_header "Optional Tools"

check_command "docker" "Docker" "24.0" "docker --version" "https://docs.docker.com/get-docker/"

print_check "Docker Compose"
if docker compose version &> /dev/null; then
    COMPOSE_VERSION=$(docker compose version 2>&1)
    print_ok "Docker Compose installed ($COMPOSE_VERSION)"
    RESULTS["Docker Compose"]="ok"
else
    print_warn "Docker Compose not available"
    RESULTS["Docker Compose"]="missing"
fi

check_command "direnv" "direnv" "2.0" "direnv --version" "https://direnv.net"

check_command "golangci-lint" "golangci-lint" "" "golangci-lint --version" "go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"

check_command "slither" "Slither" "" "slither --version" "pip3 install slither-analyzer"

# ============================================================================
# Environment Configuration
# ============================================================================
print_header "Environment Configuration"

# Check .nvmrc
print_check "Node version configuration"
if [[ -f "$REPO_ROOT/.nvmrc" ]]; then
    NVMRC_VERSION=$(cat "$REPO_ROOT/.nvmrc")
    CURRENT_NODE=$(node --version 2>/dev/null | tr -d 'v')
    
    if [[ "$CURRENT_NODE" == "$NVMRC_VERSION"* ]]; then
        print_ok "Node version matches .nvmrc (v$NVMRC_VERSION)"
        RESULTS["Node version"]="ok"
    else
        print_warn "Node version mismatch"
        echo -e "      ${YELLOW}Expected: v$NVMRC_VERSION, Found: v$CURRENT_NODE${NC}"
        echo -e "      ${YELLOW}Run: nvm use${NC}"
        RESULTS["Node version"]="mismatch"
    fi
else
    print_info ".nvmrc not found"
    RESULTS["Node version"]="unknown"
fi

# Check Go modules
print_check "Go modules"
if [[ -f "$REPO_ROOT/go.mod" ]]; then
    if go mod verify &> /dev/null; then
        print_ok "Go modules verified"
        RESULTS["Go modules"]="ok"
    else
        print_warn "Go module verification failed"
        echo -e "      ${YELLOW}Run: go mod download${NC}"
        RESULTS["Go modules"]="error"
        
        if [[ "$FIX_MODE" == "true" ]]; then
            echo -e "      ${CYAN}Attempting to fix...${NC}"
            go mod download
        fi
    fi
else
    print_info "go.mod not found"
    RESULTS["Go modules"]="unknown"
fi

# Check npm dependencies
print_check "npm dependencies"
if [[ -f "$REPO_ROOT/pnpm-lock.yaml" ]]; then
    if [[ -d "$REPO_ROOT/node_modules" ]]; then
        print_ok "node_modules exists"
        RESULTS["npm deps"]="ok"
    else
        print_warn "node_modules not found"
        echo -e "      ${YELLOW}Run: pnpm install${NC}"
        RESULTS["npm deps"]="missing"
        
        if [[ "$FIX_MODE" == "true" ]]; then
            echo -e "      ${CYAN}Attempting to fix...${NC}"
            pnpm install
        fi
    fi
else
    print_info "pnpm-lock.yaml not found"
    RESULTS["npm deps"]="unknown"
fi

# Check git submodules
print_check "Git submodules"
if [[ -f "$REPO_ROOT/.gitmodules" ]]; then
    UNINIT_SUBMODULES=$(git submodule status | grep '^-' | wc -l | tr -d ' ')
    if [[ "$UNINIT_SUBMODULES" -eq 0 ]]; then
        print_ok "All git submodules initialized"
        RESULTS["Submodules"]="ok"
    else
        print_warn "$UNINIT_SUBMODULES submodule(s) not initialized"
        echo -e "      ${YELLOW}Run: git submodule update --init --recursive${NC}"
        RESULTS["Submodules"]="incomplete"
        
        if [[ "$FIX_MODE" == "true" ]]; then
            echo -e "      ${CYAN}Attempting to fix...${NC}"
            git submodule update --init --recursive
        fi
    fi
else
    print_info "No .gitmodules file found"
    RESULTS["Submodules"]="none"
fi

# ============================================================================
# Disk Space Check
# ============================================================================
print_header "System Resources"

print_check "Available disk space"
AVAILABLE_SPACE=$(df -h "$REPO_ROOT" | awk 'NR==2 {print $4}')
AVAILABLE_BYTES=$(df "$REPO_ROOT" | awk 'NR==2 {print $4}')

# Require at least 10GB (10485760 KB)
if [[ "$AVAILABLE_BYTES" -gt 10485760 ]]; then
    print_ok "Disk space: $AVAILABLE_SPACE available"
    RESULTS["Disk space"]="ok"
else
    print_warn "Low disk space: $AVAILABLE_SPACE available"
    echo -e "      ${YELLOW}At least 10GB recommended for builds${NC}"
    RESULTS["Disk space"]="low"
fi

# ============================================================================
# Summary
# ============================================================================
print_header "Summary"

if [[ "$JSON_OUTPUT" == "true" ]]; then
    # Output JSON for CI integration
    echo "{"
    echo "  \"errors\": $ERRORS,"
    echo "  \"warnings\": $WARNINGS,"
    echo "  \"checks\": {"
    first=true
    for key in "${!RESULTS[@]}"; do
        if [[ "$first" == "true" ]]; then
            first=false
        else
            echo ","
        fi
        echo -n "    \"$key\": \"${RESULTS[$key]}\""
    done
    echo ""
    echo "  }"
    echo "}"
else
    if [[ $ERRORS -eq 0 ]] && [[ $WARNINGS -eq 0 ]]; then
        echo -e "${GREEN}${BOLD}✓ All checks passed! Your development environment is ready.${NC}"
    elif [[ $ERRORS -eq 0 ]]; then
        echo -e "${YELLOW}${BOLD}! Environment is usable with $WARNINGS warning(s).${NC}"
        echo -e "  Review warnings above for optimal setup."
    else
        echo -e "${RED}${BOLD}✗ Found $ERRORS error(s) and $WARNINGS warning(s).${NC}"
        echo -e "  Please fix errors before proceeding."
        echo ""
        echo -e "  ${CYAN}Tip: Run with --fix to attempt automatic fixes${NC}"
    fi
    
    echo ""
    echo -e "${BLUE}Quick fixes:${NC}"
    echo -e "  pnpm install           - Install npm dependencies"
    echo -e "  go mod download        - Download Go modules"
    echo -e "  pnpm update:foundry    - Update Foundry to pinned version"
    echo -e "  git submodule update --init --recursive - Initialize submodules"
fi

# Exit with appropriate code
if [[ $ERRORS -gt 0 ]]; then
    exit 1
fi
exit 0
