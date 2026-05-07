#!/bin/bash
#
# lib.sh - 公共 Shell 函数库
# 被 backup.sh 和 restore.sh 引用
#
set -euo pipefail

# ========== 脚本目录 ==========
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ========== 备份文件命名常量 ==========
BACKUP_PREFIX="target_database_"
TIMESTAMP_FORMAT="%Y%m%d%H%M"
TIMESTAMP_PATTERN='[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]'

# ========== 数据库配置 ==========
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-3306}"
DB_NAME="${DB_NAME:-migration_example_mysql}"
DB_USER="${DB_USER:-root}"
DB_PASS="${DB_PASS:-your_password_here}"

# ========== 备份目录 ==========
BACKUP_DIR="${BACKUP_DIR:-$SCRIPT_DIR}"

# ========== 颜色输出 ==========
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo_step() { echo -e "${GREEN}[${1}]${NC} $2"; }
echo_info() { echo -e "${BLUE}[Info]${NC} $1"; }
echo_warn() { echo -e "${YELLOW}[Warn]${NC} $1"; }
echo_error() { echo -e "${RED}[Error]${NC} $1" >&2; }

# ========== 错误处理 ==========
die() {
    echo_error "$1"
    exit 1
}

# ========== 初始化备份目录 ==========
ensure_backup_dir() {
    if [[ -f "$BACKUP_DIR" ]]; then
        die "备份目录是文件而非目录: $BACKUP_DIR"
    fi
    if [[ ! -d "$BACKUP_DIR" ]]; then
        mkdir -p "$BACKUP_DIR" || die "无法创建备份目录: $BACKUP_DIR"
    fi
}

# ========== 生成时间戳 ==========
generate_timestamp() {
    date +"$TIMESTAMP_FORMAT"
}

# ========== 查找最新备份 ==========
find_latest_backup() {
    local latest_file=""
    local latest_ts=""

    for f in "$BACKUP_DIR"/${BACKUP_PREFIX}${TIMESTAMP_PATTERN}.sql; do
        [[ -f "$f" ]] || continue

        local ts
        ts=$(basename "$f" | sed "s/${BACKUP_PREFIX}//; s/.sql//")

        if [[ -z "$latest_ts" ]] || [[ "$ts" > "$latest_ts" ]]; then
            latest_ts="$ts"
            latest_file="$f"
        fi
    done

    [[ -n "$latest_file" ]] && echo "$latest_file"
}

# ========== 备份文件路径 ==========
backup_file_path() {
    local ts="$1"
    echo "$BACKUP_DIR/${BACKUP_PREFIX}${ts}.sql"
}