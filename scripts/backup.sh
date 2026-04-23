#!/bin/bash
#
# backup.sh
# 备份目标 MySQL 数据库完整状态
#
# 用法:
#   ./scripts/backup.sh                  # 使用 scripts 目录作为备份目录
#   BACKUP_DIR=/path/to/dir ./backup.sh # 通过环境变量指定备份目录
#
set -euo pipefail

# ========== 数据库配置 ==========
DB_HOST="127.0.0.1"
DB_PORT="3306"
DB_NAME="migration_example_mysql"
DB_USER="root"
DB_PASS="your_password_here"

# ========== 备份目录（默认 $SCRIPT_DIR）==========
# 可通过环境变量 BACKUP_DIR 覆盖
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_DIR="${BACKUP_DIR:-$SCRIPT_DIR}"

# ========== 颜色输出 ==========
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo_step() { echo -e "${GREEN}[Backup]${NC} $1"; }
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
            --help|-h)
                cat << EOF
用法: ./backup.sh

  备份文件将保存在 scripts/ 目录下

示例:
  cd scripts && ./backup.sh          # 进入 scripts 目录执行
  ./scripts/backup.sh                 # 从项目根目录执行
  BACKUP_DIR=/path/to/dir ./backup.sh # 通过环境变量指定备份目录
EOF
                exit 0
                ;;
            *)
                shift
                ;;
        esac
    done
}

# ========== 前置检查 ==========
check_prerequisites() {
    # 检查 mysqldump
    if ! command -v mysqldump &> /dev/null; then
        die "未找到 mysqldump 命令，请确保 MySQL 客户端已安装"
    fi

    # 检查备份目录
    if [[ -f "$BACKUP_DIR" ]]; then
        die "备份目录是文件而非目录: $BACKUP_DIR"
    fi
    if [[ ! -d "$BACKUP_DIR" ]]; then
        mkdir -p "$BACKUP_DIR" || die "无法创建备份目录: $BACKUP_DIR"
    fi
}

# ========== 备份函数 ==========
do_backup() {
    local timestamp
    timestamp=$(date +"%Y%m%d%H%M")  # 格式: YYYYMMDDHHMM（12位，精确到分钟）
    local backup_file="$BACKUP_DIR/target_database_$timestamp.sql"

    # 检查备份文件是否已存在
    if [[ -f "$backup_file" ]]; then
        die "备份文件已存在: $backup_file"
    fi

    # 确保目录存在
    mkdir -p "$BACKUP_DIR"

    echo_step "开始备份数据库 $DB_NAME 到 $backup_file"
    local start_time=$(date +%s)

    # 执行备份（使用环境变量传递密码，避免命令行暴露）
    # 使用子 shell 捕获退出码
    set +e  # 临时关闭 -e，避免管道失败导致脚本退出
    export MYSQL_PWD="$DB_PASS"
    { mysqldump --host="$DB_HOST" \
                --port="$DB_PORT" \
                --user="$DB_USER" \
                --single-transaction \
                --quick \
                --no-tablespaces \
                --set-gtid-purged=OFF \
                --databases "$DB_NAME"; } > "$backup_file" 2>&1
    local dump_status=$?
    unset MYSQL_PWD
    set -e  # 重新开启 -e

    if [[ $dump_status -ne 0 ]]; then
        rm -f "$backup_file"
        die "备份失败，请检查数据库连接和权限"
    fi

    local end_time=$(date +%s)
    local duration=$((end_time - start_time))
    local size=$(du -h "$backup_file" | cut -f1)

    echo_step "备份完成，耗时 ${duration}s，文件大小 $size"
    echo_info "备份文件: $backup_file"
}

# ========== 主流程 ==========
main() {
    parse_args "$@"
    check_prerequisites
    do_backup
}

main "$@"
