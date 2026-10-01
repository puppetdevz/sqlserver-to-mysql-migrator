#!/bin/bash
#
# build.sh
# 交叉编译多平台单文件可执行程序
#
# 用法:
#   ./scripts/build.sh              # 构建所有平台
#   ./scripts/build.sh darwin      # 仅构建 macOS
#   ./scripts/build.sh linux        # 仅构建 Linux
#   ./scripts/build.sh linux-arm64  # 仅构建 Linux ARM64
#   ./scripts/build.sh linux-amd64  # 仅构建 Linux x86_64
#
# 产物输出到 dist/ 目录
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$PROJECT_DIR/dist"
BUILD_VARIANT="${BUILD_VARIANT:-}"
if [[ -n "$BUILD_VARIANT" ]]; then
    if [[ ! "$BUILD_VARIANT" =~ ^[a-zA-Z0-9_-]+$ ]]; then
        echo "Invalid BUILD_VARIANT (use letters, digits, underscore or dash)" >&2
        exit 1
    fi
    DIST_DIR="$DIST_DIR/$BUILD_VARIANT"
fi
MAIN_PACKAGE="./cmd/migrate"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo_step() {
    echo -e "${GREEN}[Build]${NC} $1"
}

echo_info() {
    echo -e "${BLUE}[Info]${NC} $1"
}

echo_warn() {
    echo -e "${YELLOW}[Warn]${NC} $1"
}

echo_error() {
    echo -e "${RED}[Error]${NC} $1"
}

# ========== 配置 ==========
PLATFORMS=(
    "darwin:arm64:macOS ARM64"
    "linux:amd64:Linux x86_64"
    "linux:arm64:Linux ARM64"
)

BUILD_TARGET="${1:-all}"

# ========== 帮助 ==========
show_help() {
    cat << EOF
用法: $0 [平台]

可用平台:
  all          构建所有平台 (默认)
  darwin       macOS ARM64
  linux        Linux AMD64 + ARM64
  linux-amd64  Linux x86_64
  linux-arm64  Linux ARM64

示例:
  $0              # 构建所有平台
  $0 darwin       # 仅构建 macOS 版本
  $0 linux-amd64  # 仅构建 Linux x86_64
EOF
}

# ========== 平台映射 ==========
should_build() {
    local platform="$1"
    case "$BUILD_TARGET" in
        all)
            return 0
            ;;
        darwin)
            [[ "$platform" == "darwin"* ]] && return 0
            ;;
        linux)
            [[ "$platform" == "linux"* ]] && return 0
            ;;
        linux-amd64)
            [[ "$platform" == "linux:amd64" ]] && return 0
            ;;
        linux-arm64)
            [[ "$platform" == "linux:arm64" ]] && return 0
            ;;
        help|-h|--help)
            show_help
            exit 0
            ;;
        *)
            echo_error "未知平台: $BUILD_TARGET"
            show_help
            exit 1
            ;;
    esac
    return 1
}

# ========== 构建单个平台 ==========
build_platform() {
    local os="$1"
    local arch="$2"
    local desc="$3"

    echo_step "开始构建 ${desc}..."

    local output_name="sqlserver-to-mysql-migrator-${os}-${arch}"

    local binary="$DIST_DIR/$output_name"

    cd "$PROJECT_DIR"

    # Prevent a failed build from being mistaken for a stale successful binary.
    rm -f "$binary" "${binary}.sha256"
    local build_tags=""
    [[ "$BUILD_VARIANT" == "p0-baseline" ]] && build_tags="migration_baseline"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -tags="$build_tags" \
        -ldflags="-s -w -X main.BuildLabel=${BUILD_VARIANT:-p1}" \
        -trimpath \
        -o "$binary" \
        "$MAIN_PACKAGE"

    if [ ! -f "$binary" ]; then
        echo_error "构建失败，产物不存在: $binary"
        return 1
    fi

    # Strip 减小体积
    strip "$binary" 2>/dev/null || true

    # SHA256 校验和
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$binary" > "${binary}.sha256"
    else
        shasum -a 256 "$binary" > "${binary}.sha256"
    fi

    local size=$(du -h "$binary" | cut -f1)
    local arch_info=$(file "$binary")
    local sha256=$(cut -d' ' -f1 "${binary}.sha256")

    echo_info "  产物: $binary"
    echo_info "  大小: $size"
    echo_info "  SHA256: $sha256"

    return 0
}

# ========== 主流程 ==========
main() {
    echo ""
    echo "========================================"
    echo "       多平台交叉编译构建脚本"
    echo "========================================"
    echo ""

    # 解析平台描述
    local platforms_to_build=()
    for p in "${PLATFORMS[@]}"; do
        IFS=':' read -r os arch desc <<< "$p"
        if should_build "$os:$arch"; then
            platforms_to_build+=("$os:$arch:$desc")
        fi
    done

    if [ ${#platforms_to_build[@]} -eq 0 ]; then
        echo_error "没有需要构建的平台"
        show_help
        exit 1
    fi

    # Keep other platform/variant artifacts and local benchmark evidence.
    echo_step "准备产物目录..."
    mkdir -p "$DIST_DIR"

    # 构建
    local success=0
    local failed=()
    for p in "${platforms_to_build[@]}"; do
        IFS=':' read -r os arch desc <<< "$p"
        if build_platform "$os" "$arch" "$desc"; then
            success=$((success + 1))
        else
            failed+=("$desc")
        fi
        echo ""
    done

    # 汇总
    echo "========================================"
    echo -e "${GREEN}构建完成${NC}"
    echo "========================================"
    echo "成功: $success/${#platforms_to_build[@]}"
    if [ ${#failed[@]} -gt 0 ]; then
        echo -e "${RED}失败:${NC} ${failed[*]}"
    fi
    echo ""
    echo "产物目录: $DIST_DIR"
    echo ""
    echo "使用方式:"
    echo "  ./dist/sqlserver-to-mysql-migrator-darwin-arm64 --help   # macOS"
    echo "  ./dist/sqlserver-to-mysql-migrator-linux-amd64 --help   # Linux x86_64"
    echo "  ./dist/sqlserver-to-mysql-migrator-linux-arm64 --help   # Linux ARM64"
    echo "========================================"

    if [ ${#failed[@]} -gt 0 ]; then
        exit 1
    fi
}

main
