#!/usr/bin/env bash
# Pre-commit hook for the Optimism monorepo
# This script runs code quality checks before allowing commits
#
# Usage:
#   ./ops/scripts/pre-commit.sh [--install] [--quick]
#
# Options:
#   --install   Install this script as a git pre-commit hook
#   --quick     Run only fast checks (formatting, no tests)
#
# To install as a git hook:
#   ./ops/scripts/pre-commit.sh --install

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
QUICK_MODE=false
INSTALL_MODE=false

# Parse arguments
for arg in "$@"; do
    case $arg in
        --quick)
            QUICK_MODE=true
            shift
            ;;
        --install)
            INSTALL_MODE=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [--install] [--quick]"
            echo ""
            echo "Options:"
            echo "  --install   Install this script as a git pre-commit hook"
            echo "  --quick     Run only fast checks (formatting, no tests)"
            exit 0
            ;;
    esac
done

# Install as git hook if requested
if [[ "$INSTALL_MODE" == "true" ]]; then
    HOOK_PATH="$REPO_ROOT/.git/hooks/pre-commit"
    
    cat > "$HOOK_PATH" << 'EOF'
#!/usr/bin/env bash
# Auto-generated pre-commit hook - do not edit directly
# To update, run: ./ops/scripts/pre-commit.sh --install

exec "$(git rev-parse --show-toplevel)/ops/scripts/pre-commit.sh" --quick
EOF
    
    chmod +x "$HOOK_PATH"
    echo -e "${GREEN}✓${NC} Pre-commit hook installed at $HOOK_PATH"
    exit 0
fi

# Utility functions
print_header() {
    echo ""
    echo -e "${BLUE}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${CYAN}${BOLD}  $1${NC}"
    echo -e "${BLUE}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

print_step() {
    echo -e "${YELLOW}▶${NC} $1"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}!${NC} $1"
}

# Track failed checks
FAILED_CHECKS=()

run_check() {
    local name="$1"
    local cmd="$2"
    
    print_step "Running: $name"
    
    if eval "$cmd" > /dev/null 2>&1; then
        print_success "$name passed"
        return 0
    else
        print_error "$name failed"
        FAILED_CHECKS+=("$name")
        return 1
    fi
}

# Change to repo root
cd "$REPO_ROOT"

print_header "Optimism Pre-Commit Checks"
echo -e "Mode: ${CYAN}$(if $QUICK_MODE; then echo "Quick"; else echo "Full"; fi)${NC}"
echo -e "Repository: ${CYAN}$REPO_ROOT${NC}"

# Get list of staged Go files
STAGED_GO_FILES=$(git diff --cached --name-only --diff-filter=ACM | grep '\.go$' || true)
STAGED_SOL_FILES=$(git diff --cached --name-only --diff-filter=ACM | grep '\.sol$' || true)
STAGED_TS_FILES=$(git diff --cached --name-only --diff-filter=ACM | grep -E '\.(ts|tsx|js|jsx)$' || true)

# ============================================================================
# Check 1: Go Formatting
# ============================================================================
if [[ -n "$STAGED_GO_FILES" ]]; then
    print_header "Go Formatting Check"
    
    UNFORMATTED_FILES=""
    for file in $STAGED_GO_FILES; do
        if [[ -f "$file" ]]; then
            if ! gofmt -l "$file" | grep -q '^'; then
                : # File is formatted
            else
                UNFORMATTED_FILES="$UNFORMATTED_FILES $file"
            fi
        fi
    done
    
    if [[ -z "$UNFORMATTED_FILES" ]]; then
        print_success "All Go files are properly formatted"
    else
        print_error "The following Go files need formatting:"
        for f in $UNFORMATTED_FILES; do
            echo "    - $f"
        done
        echo ""
        echo -e "${YELLOW}Run 'gofmt -w <file>' to fix formatting${NC}"
        FAILED_CHECKS+=("Go formatting")
    fi
fi

# ============================================================================
# Check 2: Go Imports
# ============================================================================
if [[ -n "$STAGED_GO_FILES" ]] && command -v goimports &> /dev/null; then
    print_header "Go Imports Check"
    
    UNIMPORTED_FILES=""
    for file in $STAGED_GO_FILES; do
        if [[ -f "$file" ]]; then
            if ! goimports -l "$file" | grep -q '^'; then
                : # File imports are sorted
            else
                UNIMPORTED_FILES="$UNIMPORTED_FILES $file"
            fi
        fi
    done
    
    if [[ -z "$UNIMPORTED_FILES" ]]; then
        print_success "All Go imports are properly organized"
    else
        print_warning "The following Go files may have unorganized imports:"
        for f in $UNIMPORTED_FILES; do
            echo "    - $f"
        done
        echo ""
        echo -e "${YELLOW}Run 'goimports -w <file>' to fix imports${NC}"
    fi
fi

# ============================================================================
# Check 3: Go Vet (quick static analysis)
# ============================================================================
if [[ -n "$STAGED_GO_FILES" ]]; then
    print_header "Go Vet Check"
    
    # Get unique directories containing changed Go files
    CHANGED_DIRS=$(echo "$STAGED_GO_FILES" | xargs -I{} dirname {} | sort -u)
    
    VET_FAILED=false
    for dir in $CHANGED_DIRS; do
        if [[ -d "$dir" ]]; then
            if ! go vet "./$dir/..." 2>/dev/null; then
                VET_FAILED=true
            fi
        fi
    done
    
    if [[ "$VET_FAILED" == "false" ]]; then
        print_success "Go vet passed"
    else
        print_error "Go vet found issues"
        FAILED_CHECKS+=("Go vet")
    fi
fi

# ============================================================================
# Check 4: Check for common issues
# ============================================================================
print_header "Common Issues Check"

# Check for debug statements
DEBUG_PATTERNS="fmt\.Println|log\.Print|console\.log|debugger"
DEBUG_FILES=""

for file in $STAGED_GO_FILES $STAGED_TS_FILES; do
    if [[ -f "$file" ]]; then
        if grep -E "$DEBUG_PATTERNS" "$file" > /dev/null 2>&1; then
            DEBUG_FILES="$DEBUG_FILES $file"
        fi
    fi
done

if [[ -z "$DEBUG_FILES" ]]; then
    print_success "No debug statements found"
else
    print_warning "Possible debug statements found in:"
    for f in $DEBUG_FILES; do
        echo "    - $f"
    done
    echo -e "${YELLOW}Please review and remove if unintended${NC}"
fi

# Check for TODO without issue reference
if [[ -n "$STAGED_GO_FILES" ]]; then
    INVALID_TODOS=""
    for file in $STAGED_GO_FILES; do
        if [[ -f "$file" ]]; then
            # Check for TODOs that don't follow the format TODO(issue#) or TODO(username)
            if grep -n "TODO[^(]" "$file" > /dev/null 2>&1; then
                INVALID_TODOS="$INVALID_TODOS $file"
            fi
        fi
    done
    
    if [[ -n "$INVALID_TODOS" ]]; then
        print_warning "TODOs without issue reference found in:"
        for f in $INVALID_TODOS; do
            echo "    - $f"
        done
        echo -e "${YELLOW}Consider using format: TODO(<issue_number>): description${NC}"
    fi
fi

# ============================================================================
# Check 5: Run Go tests (only in full mode)
# ============================================================================
if [[ "$QUICK_MODE" == "false" ]] && [[ -n "$STAGED_GO_FILES" ]]; then
    print_header "Go Tests"
    
    # Get unique modules containing changed Go files
    CHANGED_DIRS=$(echo "$STAGED_GO_FILES" | xargs -I{} dirname {} | sort -u)
    
    for dir in $CHANGED_DIRS; do
        if [[ -d "$dir" ]] && [[ -f "$dir/go.mod" || -f "$(dirname "$dir")/go.mod" ]]; then
            print_step "Testing $dir..."
            if go test -short "./$dir/..." 2>/dev/null; then
                print_success "Tests passed for $dir"
            else
                print_error "Tests failed for $dir"
                FAILED_CHECKS+=("Go tests: $dir")
            fi
        fi
    done
fi

# ============================================================================
# Check 6: TypeScript/JavaScript checks
# ============================================================================
if [[ -n "$STAGED_TS_FILES" ]] && command -v pnpm &> /dev/null; then
    print_header "TypeScript/JavaScript Checks"
    
    # Check if there are any relevant package.json files
    TS_PACKAGES=$(echo "$STAGED_TS_FILES" | xargs -I{} dirname {} | sort -u)
    
    for pkg in $TS_PACKAGES; do
        if [[ -f "$pkg/package.json" ]]; then
            print_step "Checking TypeScript in $pkg..."
            if (cd "$pkg" && pnpm typecheck 2>/dev/null); then
                print_success "TypeScript check passed for $pkg"
            else
                print_warning "TypeScript check skipped or failed for $pkg"
            fi
        fi
    done
fi

# ============================================================================
# Check 7: Solidity formatting (if forge is available)
# ============================================================================
if [[ -n "$STAGED_SOL_FILES" ]] && command -v forge &> /dev/null; then
    print_header "Solidity Checks"
    
    print_step "Checking Solidity formatting..."
    if forge fmt --check 2>/dev/null; then
        print_success "Solidity files are properly formatted"
    else
        print_error "Solidity files need formatting"
        echo -e "${YELLOW}Run 'forge fmt' to fix formatting${NC}"
        FAILED_CHECKS+=("Solidity formatting")
    fi
fi

# ============================================================================
# Summary
# ============================================================================
print_header "Summary"

if [[ ${#FAILED_CHECKS[@]} -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}All checks passed! ✓${NC}"
    echo ""
    exit 0
else
    echo -e "${RED}${BOLD}The following checks failed:${NC}"
    for check in "${FAILED_CHECKS[@]}"; do
        echo -e "  ${RED}✗${NC} $check"
    done
    echo ""
    echo -e "${YELLOW}Please fix these issues before committing.${NC}"
    echo -e "${YELLOW}Use --quick flag to run only fast checks.${NC}"
    exit 1
fi
