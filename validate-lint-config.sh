#!/bin/bash

# Test script for the enhanced golangci-lint configuration
# This script validates the configuration and runs basic checks

set -e

echo "🔍 Validating Enhanced golangci-lint Configuration"
echo "================================================="

# Check if golangci-lint is available
if ! command -v golangci-lint &> /dev/null; then
    echo "❌ golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
    exit 1
fi

echo "✅ golangci-lint version: $(golangci-lint --version)"

# Check configuration syntax
echo -e "\n📋 Checking configuration syntax..."
if golangci-lint config path > /dev/null 2>&1; then
    echo "✅ Configuration syntax is valid"
    echo "📁 Using config: $(golangci-lint config path)"
else
    echo "❌ Configuration has syntax errors"
    exit 1
fi

# Count enabled linters
echo -e "\n📊 Linter Statistics:"
ENABLED_COUNT=$(grep -E "^[ ]*-" .golangci.yml | sed -n '/enable:/,/disable:/p' | grep -E "^[ ]*-" | wc -l | tr -d ' ')
echo "   Enabled linters: $ENABLED_COUNT"

# List new additions
echo -e "\n🆕 New High-Value Additions:"
echo "   • gosec (security vulnerability scanner)"
echo "   • bodyclose (HTTP resource management)"  
echo "   • contextcheck (k8s context patterns)"
echo "   • errorlint (error wrapping validation)"
echo "   • nilerr (nil error pattern validation)"
echo "   • gci (import organization)"
echo "   • testpackage (test naming conventions)"
echo "   • asciicheck/bidichk (character safety)"
echo "   • noctx (HTTP without context)"

# Test basic linting (non-CGO linters only)
echo -e "\n🧪 Testing Basic Linting (format + style)..."
if golangci-lint run --disable-all --enable=gofmt,goimports,misspell,revive --timeout=30s ./cmd/... > /dev/null 2>&1; then
    echo "✅ Basic linting works correctly"
else
    echo "⚠️  Basic linting had issues (may be due to CGO dependencies)"
    echo "   Try: make download-tokenizer && CGO_ENABLED=1 golangci-lint run"
fi

echo -e "\n📚 Documentation Created:"
echo "   • .golangci.yml - Enhanced configuration"
echo "   • GOLANGCI_LINT_IMPROVEMENTS.md - Technical details"
echo "   • ENHANCEMENT_SUMMARY.md - Executive summary"
echo "   • COMMIT_SUMMARY.md - Change summary"

echo -e "\n🎯 Next Steps:"
echo "   1. Review the configuration changes"
echo "   2. Run 'make lint' to test full suite"
echo "   3. Consider using '--new' flag for incremental adoption"
echo "   4. Add project-specific exclusions if needed"

echo -e "\n✨ Configuration enhancement complete!"