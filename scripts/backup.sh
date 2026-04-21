#!/bin/bash
#
# backup.sh
# 备份目标 MySQL 数据库完整状态
#
# 用法:
#   ./scripts/backup.sh                  # 使用默认 config.yaml
#   ./scripts/backup.sh --config /path    # 指定配置文件
#
set -euo pipefail

# ========== 配置 ==========
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$SCRIPT_DIR/config.yaml"
BACKUP_DIR="$SCRIPT_DIR/../data"

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
            --config)
                CONFIG_FILE="$2"
                shift 2
                ;;
            --help|-h)
                cat << EOF
用法: $0 [--config /path/to/config.yaml]

  --config    指定配置文件（默认: scripts/config.yaml）
  --help, -h  显示帮助信息
EOF
                exit 0
                ;;
            *)
                shift
                ;;
        esac
    done
}

# ========== 配置解析 ==========
# config.yaml 使用嵌套结构（target.host, target.port 等）
parse_yaml_value() {
    local key="$1"
    case "$key" in
        host|port|database|user|password)
            # 提取 target.* 字段，在 target: 缩进下查找对应 key
            awk -v k="$key" '
                /^target:/ { in_target=1; target_indent=length($0) - length($0##[[:space:]]); next }
                in_target && /^[^[:space:]]/ { in_target=0 }
                in_target && /^[[:space:]]/ {
                    cur_indent=length($0) - length($0##[[:space:]])
                    if (cur_indent > target_indent && $0 ~ "^[[:space:]]*" k ":") {
                        sub(/^[[:space:]]*[^:]*:[[:space:]]*/, "")
                        gsub(/^[ \t]+|[ \t]+$/, "")
                        gsub(/^["'\'']|["'\'']$/g, "")
                        print
                        exit
                    }
                }
            ' "$CONFIG_FILE"
            ;;
        *)
            grep "^[[:space:]]*${key}:" "$CONFIG_FILE" | sed 's/.*:[[:space:]]*//' | tr -d '"' | tr -d "'"
            ;;
    esac
}

# ========== 前置检查 ==========
check_prerequisites() {
    # 检查配置文件
    if [[ ! -f "$CONFIG_FILE" ]]; then
        die "配置文件不存在: $CONFIG_FILE"
    fi

    # 检查 mysqldump
    if ! command -v mysqldump &> /dev/null; then
        die "未找到 mysqldump 命令，请确保 MySQL 客户端已安装"
    fi

    # 解析配置
    local DB_HOST DB_PORT DB_NAME DB_USER DB_PASS
    DB_HOST=$(parse_yaml_value "host")
    DB_PORT=$(parse_yaml_value "port")
    DB_NAME=$(parse_yaml_value "database")
    DB_USER=$(parse_yaml_value "user")
    DB_PASS=$(parse_yaml_value "password")

    # 检查必要字段
    local missing_fields=()
    [[ -z "$DB_HOST" ]] && missing_fields+=("target.host")
    [[ -z "$DB_PORT" ]] && missing_fields+=("target.port")
    [[ -z "$DB_NAME" ]] && missing_fields+=("target.database")
    [[ -z "$DB_USER" ]] && missing_fields+=("target.user")
    [[ -z "$DB_PASS" ]] && missing_fields+=("target.password")
    if [[ ${#missing_fields[@]} -gt 0 ]]; then
        die "配置字段缺失: ${missing_fields[*]}"
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

    # 执行备份（使用子进程保存退出码）
    {
        mysqldump --host="$DB_HOST" \
                  --port="$DB_PORT" \
                  --user="$DB_USER" \
                  --password="$DB_PASS" \
                  --single-transaction \
                  --quick \
                  --set-gtid-purged=OFF \
                  --databases "$DB_NAME"
    } > "$backup_file" 2>&1
    local dump_status=$?

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

main