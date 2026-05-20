#!/bin/bash
#
# backup.sh - 备份目标 MySQL 数据库完整状态
#
# 用法:
#   ./backup.sh                  # 使用脚本目录作为备份目录
#   BACKUP_DIR=/path/to/dir ./backup.sh
#
# 环境变量:
#   DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS (必填)
#   BACKUP_DIR - 备份目录（默认: 脚本所在目录）
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

# ========== 参数解析 ==========
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --help|-h)
                cat << EOF
用法: ./backup.sh

示例:
  cd scripts && ./backup.sh
  DB_PASS=xxx BACKUP_DIR=/path ./backup.sh
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
    check_cmd mysqldump "请确保 MySQL 客户端已安装"
    [[ -z "$DB_PASS" ]] && die "请设置 DB_PASS 环境变量"
    ensure_backup_dir
}

# ========== 备份函数 ==========
do_backup() {
    local ts
    ts=$(generate_timestamp)
    local backup_file
    backup_file=$(backup_file_path "$ts")

    if [[ -f "$backup_file" ]]; then
        die "备份文件已存在: $backup_file"
    fi

    echo_step "Backup" "开始备份数据库 $DB_NAME 到 $backup_file"
    local start_time=$(date +%s)

    if ! run_mysqldump "$backup_file"; then
        if [[ -f "$backup_file" ]]; then
            rm "$backup_file"
        fi
        die "备份失败，请检查数据库连接和权限"
    fi

    local duration=$(calc_duration "$start_time")
    local size=$(du -h "$backup_file" | cut -f1)

    echo_step "Backup" "备份完成，耗时 ${duration}s，文件大小 $size"
    echo_info "备份文件: $backup_file"
}

# ========== 主流程 ==========
main() {
    parse_args "$@"
    check_prerequisites
    do_backup
}

main "$@"
