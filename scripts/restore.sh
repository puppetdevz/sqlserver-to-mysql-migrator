#!/bin/bash
#
# restore.sh
# 从 SQL 备份文件恢复 MySQL 数据库
#
# 用法:
#   ./scripts/restore.sh                     # 恢复最新备份
#   ./scripts/restore.sh --timestamp 202604211200   # 恢复指定备份
#
# 环境变量:
#   BACKUP_DIR - 备份目录（默认: 脚本所在目录）
#
set -euo pipefail

# ========== 脚本目录 ==========
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ========== 数据库配置 ==========
DB_HOST="127.0.0.1"
DB_PORT="3306"
DB_NAME="migration_example_mysql"
DB_USER="root"
DB_PASS="your_password_here"

# ========== 备份目录（默认 $SCRIPT_DIR）==========
BACKUP_DIR="${BACKUP_DIR:-$SCRIPT_DIR}"

# ========== 颜色输出 ==========
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo_step() { echo -e "${GREEN}[Restore]${NC} $1"; }
echo_info() { echo -e "${BLUE}[Info]${NC} $1"; }
echo_warn() { echo -e "${YELLOW}[Warn]${NC} $1"; }
echo_error() { echo -e "${RED}[Error]${NC} $1" >&2; }

# ========== 错误处理 ==========
die() {
    echo_error "$1"
    exit 1
}

# ========== 参数解析 ==========
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --timestamp)
                BACKUP_TIMESTAMP="$2"
                shift 2
                ;;
            --help|-h)
                cat << EOF
用法: ./restore.sh
     ./restore.sh --timestamp 202604211200

  不指定 --timestamp 则自动选择时间戳最新的备份文件

示例:
  cd scripts && ./restore.sh               # 恢复最新备份
  ./scripts/restore.sh --timestamp 202604211200  # 恢复指定备份
  BACKUP_DIR=/path/to/dir ./restore.sh     # 通过环境变量指定备份目录
EOF
                exit 0
                ;;
            *)
                shift
                ;;
        esac
    done
}

# ========== 查找备份文件 ==========
find_backup_file() {
    local timestamp="$1"
    local backup_pattern="$BACKUP_DIR/target_database_${timestamp}.sql"

    if [[ -f "$backup_pattern" ]]; then
        echo "$backup_pattern"
        return 0
    fi
    return 1
}

# ========== 查找最新备份 ==========
find_latest_backup() {
    local latest_file=""
    local latest_ts=""

    # 查找所有 target_database_*.sql 文件
    # 按文件名中的时间戳排序（12位数字），取最新的
    for f in "$BACKUP_DIR"/target_database_[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].sql; do
        [[ -f "$f" ]] || continue

        # 从文件名提取时间戳
        local filename="$(basename "$f")"
        local ts="${filename#target_database_}"
        ts="${ts%.sql}"

        # 比较时间戳
        if [[ -z "$latest_ts" ]] || [[ "$ts" > "$latest_ts" ]]; then
            latest_ts="$ts"
            latest_file="$f"
        fi
    done

    if [[ -n "$latest_file" ]]; then
        echo "$latest_file"
        return 0
    fi
    return 1
}

# ========== 前置检查 ==========
check_prerequisites() {
    # 检查 mysql
    if ! command -v mysql &> /dev/null; then
        die "未找到 mysql 命令，请确保 MySQL 客户端已安装"
    fi

    # 查找备份文件
    if [[ -n "${BACKUP_TIMESTAMP:-}" ]]; then
        # 指定了时间戳，查找对应文件
        BACKUP_FILE=$(find_backup_file "$BACKUP_TIMESTAMP")
        if [[ -z "$BACKUP_FILE" ]]; then
            die "备份文件不存在: $BACKUP_DIR/target_database_${BACKUP_TIMESTAMP}.sql"
        fi
    else
        # 未指定时间戳，查找最新备份
        echo_info "未指定备份时间戳，正在查找最新备份..."
        BACKUP_FILE=$(find_latest_backup)
        if [[ -z "$BACKUP_FILE" ]]; then
            die "未找到任何备份文件，请先执行备份或指定 --timestamp"
        fi
        echo_info "选择最新备份: $(basename "$BACKUP_FILE")"
    fi
}

# ========== 恢复函数 ==========
do_restore() {
    echo_step "开始恢复数据库 $DB_NAME from $(basename "$BACKUP_FILE")"
    local start_time=$(date +%s)

    # 使用环境变量传递密码，避免命令行暴露
    # 使用子 shell 捕获退出码
    set +e  # 临时关闭 -e，避免管道失败导致脚本退出
    export MYSQL_PWD="$DB_PASS"

    # 恢复数据库（使用子 shell 正确捕获 mysql 退出码）
    { mysql --host="$DB_HOST" \
            --port="$DB_PORT" \
            --user="$DB_USER" \
            --database="$DB_NAME" \
            --default-character-set=utf8mb4; } < "$BACKUP_FILE" 2>&1

    local restore_status=$?
    unset MYSQL_PWD
    set -e  # 重新开启 -e

    if [[ $restore_status -ne 0 ]]; then
        die "恢复失败，请检查数据库连接和备份文件完整性"
    fi

    local end_time=$(date +%s)
    local duration=$((end_time - start_time))

    echo_step "恢复完成，耗时 ${duration}s"
    echo_info "备份文件: $BACKUP_FILE"
}

# ========== 全局变量 ==========
BACKUP_TIMESTAMP=""
BACKUP_FILE=""

# ========== 主流程 ==========
main() {
    parse_args "$@"
    check_prerequisites
    do_restore
}

main "$@"

