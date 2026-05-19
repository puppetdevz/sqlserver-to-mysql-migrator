#!/bin/bash
#
# restore.sh - 从 SQL 备份文件恢复 MySQL 数据库
#
# 用法:
#   ./restore.sh                     # 恢复最新备份
#   ./restore.sh --timestamp YYYYMMDDHHMM   # 恢复指定备份
#   ./restore.sh --dry-run          # 预览恢复操作（不清空表）
#
# 环境变量:
#   DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS (必填)
#   BACKUP_DIR - 备份目录（默认: 脚本所在目录）
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

# ========== 全局变量 ==========
BACKUP_TIMESTAMP=""
BACKUP_FILE=""
DRY_RUN=false

# ========== 参数解析 ==========
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --timestamp)
                BACKUP_TIMESTAMP="$2"
                shift 2
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --help|-h)
                cat << EOF
用法: ./restore.sh
     ./restore.sh --timestamp YYYYMMDDHHMM
     ./restore.sh --dry-run

选项:
  --timestamp YYYYMMDDHHMM  恢复指定备份
  --dry-run                  仅预览，不执行实际恢复

示例:
  cd scripts && ./restore.sh
  ./restore.sh --timestamp 202604211830
  ./restore.sh --dry-run
  DB_PASS=xxx BACKUP_DIR=/path ./restore.sh
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
    local ts="$1"
    local path
    path=$(backup_file_path "$ts")
    [[ -f "$path" ]] && echo "$path"
}

# ========== 前置检查 ==========
check_prerequisites() {
    check_cmd mysql "请确保 MySQL 客户端已安装"
    [[ -z "$DB_PASS" ]] && die "请设置 DB_PASS 环境变量"

    if [[ -n "$BACKUP_TIMESTAMP" ]]; then
        BACKUP_FILE=$(find_backup_file "$BACKUP_TIMESTAMP")
        if [[ -z "$BACKUP_FILE" ]]; then
            die "备份文件不存在: $(backup_file_path "$BACKUP_TIMESTAMP")"
        fi
    else
        echo_info "未指定备份时间戳，正在查找最新备份..."
        BACKUP_FILE=$(find_latest_backup)
        if [[ -z "$BACKUP_FILE" ]]; then
            die "未找到任何备份文件，请先执行备份或指定 --timestamp"
        fi
        echo_info "选择最新备份: $(basename "$BACKUP_FILE")"
    fi
}

# ========== 清空所有表 ==========
drop_all_tables() {
    local sql
    sql=$(generate_drop_statements) || die "获取表列表失败"

    echo_info "即将删除以下表:"
    echo "$sql" | grep "DROP TABLE" | sed "s/DROP TABLE IF EXISTS //;s/;//" | while read -r table; do
        echo "  - $table"
    done

    if [[ "$DRY_RUN" == true ]]; then
        echo_warn "[Dry Run] 跳过实际删除操作"
        return 0
    fi

    echo_step "Restore" "清空数据库 $DB_NAME 中的所有表..."
    export MYSQL_PWD="$DB_PASS"
    echo "$sql" | mysql --host="$DB_HOST" \
                        --port="$DB_PORT" \
                        --user="$DB_USER" \
                        --database="$DB_NAME" \
                        --default-character-set=utf8mb4 2>&1
    local status=$?
    unset MYSQL_PWD

    [[ $status -ne 0 ]] && die "清空表失败"
}

# ========== 恢复函数 ==========
do_restore() {
    echo_step "Restore" "开始恢复数据库 $DB_NAME from $(basename "$BACKUP_FILE")"
    local start_time=$(date +%s)

    if [[ "$DRY_RUN" == true ]]; then
        echo_warn "[Dry Run] 跳过实际恢复操作"
        echo_info "备份文件: $BACKUP_FILE"
        return 0
    fi

    if ! run_mysql_restore "$BACKUP_FILE"; then
        die "恢复失败，请检查数据库连接和备份文件完整性"
    fi

    local duration=$(calc_duration "$start_time")

    echo_step "Restore" "恢复完成，耗时 ${duration}s"
    echo_info "恢复源文件: $BACKUP_FILE"
}

# ========== 主流程 ==========
main() {
    parse_args "$@"
    check_prerequisites
    drop_all_tables
    do_restore
}

main "$@"
