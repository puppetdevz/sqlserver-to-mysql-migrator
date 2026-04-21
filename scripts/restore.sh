#!/bin/bash
#
# restore.sh
# 从 SQL 备份文件恢复 MySQL 数据库
#
# 用法:
#   ./scripts/restore.sh                     # 使用默认 config.yaml，恢复最新备份
#   ./scripts/restore.sh --config /path      # 指定配置文件
#   ./scripts/restore.sh --timestamp 202604211200   # 指定备份时间戳
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
            --config)
                CONFIG_FILE="$2"
                shift 2
                ;;
            --timestamp)
                BACKUP_TIMESTAMP="$2"
                shift 2
                ;;
            --help|-h)
                cat << EOF
用法: $0 [--config /path/to/config.yaml] [--timestamp YYYYMMDDHHMM]

  --config     指定配置文件（默认: scripts/config.yaml）
  --timestamp  指定备份时间戳，格式: YYYYMMDDHHMM（12位）
               不指定则自动选择文件名中时间戳最新的备份
  --help, -h   显示帮助信息

示例:
  $0                                    # 恢复最新备份
  $0 --timestamp 202604211200           # 恢复指定时间戳的备份
  $0 --config /path/to/config.yaml      # 使用指定配置
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
    # 检查配置文件
    if [[ ! -f "$CONFIG_FILE" ]]; then
        die "配置文件不存在: $CONFIG_FILE"
    fi

    # 检查 mysql
    if ! command -v mysql &> /dev/null; then
        die "未找到 mysql 命令，请确保 MySQL 客户端已安装"
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
    set +e  # 临时关闭 -e，避免管道失败导致脚本退出
    export MYSQL_PWD="$DB_PASS"

    # 恢复数据库（读取 SQL 文件并执行）
    mysql --host="$DB_HOST" \
          --port="$DB_PORT" \
          --user="$DB_USER" \
          --database="$DB_NAME" \
          --default-character-set=utf8mb4 \
          < "$BACKUP_FILE" 2>&1

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

main
